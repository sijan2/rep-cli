package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const RequestTimeout = 30 * time.Second
const maxResponseBytes = 1024 * 1024
const maxRequestBytes = 48 * 1024
const maxQuestionContextBytes = 24 * 1024

type Question struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

type ChoiceAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type Evaluation struct {
	Model   string                  `json:"model"`
	Answers map[string]ChoiceAnswer `json:"answers"`
	Usage   Usage                   `json:"usage"`
}

type Client struct {
	key        string
	model      string
	httpClient *http.Client
}

func NewClient(config Config) *Client {
	model := config.Model
	if model == "" {
		model = Model
	}
	return &Client{key: config.APIKey, model: model, httpClient: &http.Client{
		Timeout:       RequestTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Evaluate makes bounded, typed choice decisions. It does not execute decisions.
func (c *Client) Evaluate(ctx context.Context, state any, questions map[string]Question) (Evaluation, error) {
	if c == nil || strings.TrimSpace(c.key) == "" {
		return Evaluation{}, errors.New("Jev key is not configured")
	}
	if !modelName.MatchString(c.model) {
		return Evaluation{}, errors.New("invalid Jev model identifier")
	}
	if len(questions) == 0 || len(questions) > 32 {
		return Evaluation{}, errors.New("Jev requires between 1 and 32 questions")
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		return Evaluation{}, errors.New("invalid Jev state")
	}
	for _, question := range questions {
		if question.Type != "choice" || strings.TrimSpace(question.Instructions) == "" || len(question.Criteria) < 2 || len(question.Criteria) > 255 {
			return Evaluation{}, errors.New("invalid Jev choice question")
		}
		questionBytes, err := json.Marshal(question)
		if err != nil || len(stateBytes)+len(questionBytes) > maxQuestionContextBytes {
			return Evaluation{}, errors.New("Jev state plus a question exceeds the conservative 24 KiB context budget")
		}
	}
	body, err := json.Marshal(struct {
		Model     string              `json:"model"`
		State     any                 `json:"state"`
		Questions map[string]Question `json:"questions"`
	}{c.model, state, questions})
	if err != nil || len(body) > maxRequestBytes {
		return Evaluation{}, errors.New("Jev request is invalid or exceeds the conservative 48 KiB request budget")
	}
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	for attempt := 0; attempt < 3; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, Endpoint, bytes.NewReader(body))
		if err != nil {
			return Evaluation{}, errors.New("cannot construct Jev request")
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+c.key)
		response, err := c.httpClient.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return Evaluation{}, errors.New("Jev request canceled or timed out")
			}
			return Evaluation{}, errors.New("Jev connection failed")
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
		response.Body.Close()
		if readErr != nil {
			return Evaluation{}, errors.New("cannot read Jev response")
		}
		if len(data) > maxResponseBytes {
			return Evaluation{}, errors.New("Jev response exceeds 1 MiB")
		}
		if response.StatusCode == http.StatusOK {
			return decodeEvaluation(data, questions)
		}
		retryable := response.StatusCode == 429 || response.StatusCode >= 500
		if !retryable || attempt == 2 {
			return Evaluation{}, fmt.Errorf("Jev API returned HTTP %d", response.StatusCode)
		}
		delay := time.Duration(1<<attempt) * 500 * time.Millisecond
		if seconds, err := strconv.ParseInt(response.Header.Get("Retry-After"), 10, 64); err == nil && seconds >= 0 {
			if seconds > int64(RequestTimeout/time.Second) {
				return Evaluation{}, errors.New("Jev retry delay exceeds request deadline; retry later")
			}
			delay = max(delay, time.Duration(seconds)*time.Second)
		} else if when, err := http.ParseTime(response.Header.Get("Retry-After")); err == nil {
			delay = max(delay, time.Until(when))
		}
		if deadline, ok := ctx.Deadline(); ok && delay >= time.Until(deadline) {
			return Evaluation{}, errors.New("Jev retry delay exceeds request deadline; retry later")
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Evaluation{}, errors.New("Jev request canceled or timed out")
		case <-timer.C:
		}
	}
	return Evaluation{}, errors.New("Jev request failed")
}

func decodeEvaluation(data []byte, questions map[string]Question) (Evaluation, error) {
	invalid := errors.New("Jev returned an invalid typed decision")
	var wire struct {
		Model   string `json:"model"`
		Answers map[string]struct {
			Type          string              `json:"type"`
			Choice        string              `json:"choice"`
			Probabilities map[string]*float64 `json:"probabilities"`
			Confidence    *float64            `json:"confidence"`
		} `json:"answers"`
		Usage *struct {
			InputTokens  *int `json:"input_tokens"`
			OutputTokens *int `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &wire) != nil || !modelName.MatchString(wire.Model) || wire.Usage == nil || wire.Usage.InputTokens == nil || wire.Usage.OutputTokens == nil || *wire.Usage.InputTokens < 0 || *wire.Usage.OutputTokens < 0 || len(wire.Answers) != len(questions) {
		return Evaluation{}, invalid
	}
	result := Evaluation{Model: wire.Model, Answers: make(map[string]ChoiceAnswer), Usage: Usage{InputTokens: *wire.Usage.InputTokens, OutputTokens: *wire.Usage.OutputTokens}}
	for name, question := range questions {
		answer, exists := wire.Answers[name]
		if !exists || answer.Type != "choice" || answer.Confidence == nil || !isProbability(*answer.Confidence) || len(answer.Probabilities) != len(question.Criteria) {
			return Evaluation{}, invalid
		}
		if _, exists := question.Criteria[answer.Choice]; !exists {
			return Evaluation{}, invalid
		}
		probabilities := make(map[string]float64)
		total, largest := 0.0, 0.0
		for option := range question.Criteria {
			probability, exists := answer.Probabilities[option]
			if !exists || probability == nil || !isProbability(*probability) {
				return Evaluation{}, invalid
			}
			probabilities[option] = *probability
			total += *probability
			largest = max(largest, *probability)
		}
		if math.Abs(total-1) > 0.001 || probabilities[answer.Choice]+0.000001 < largest {
			return Evaluation{}, invalid
		}
		result.Answers[name] = ChoiceAnswer{Type: answer.Type, Choice: answer.Choice, Probabilities: probabilities, Confidence: *answer.Confidence}
	}
	return result, nil
}

func isProbability(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}
