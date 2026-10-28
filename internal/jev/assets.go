package jev

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// Image assets and the box/noul question types follow TypeSafe's documented
// contract: https://docs.typesafe.ai/images, /primitives/box and /api/system-one.
// Images are a "danger zone" feature. Every request that carries an image asset
// or a box question must send ImagesHeader, and TypeSafe states that the request
// format, answers and limits can change between releases. Jev 1.13 and 1.14
// accept images; box questions need Jev 1.14 (jev-latest since 2026-10-14).
const ImagesHeader = "TypeSafe-Danger-Zone-Images"

// Documented image limits (inclusive). Images are never resized by the API; one
// rejected image fails the whole request with no partial answers.
const (
	MinImageSide         = 280
	MaxImageSide         = 1568
	MinImagePixels       = 78400
	MaxImagePixels       = 2458624
	MaxImageBytes        = 16 * 1024 * 1024
	MaxAssetReferenceLen = 1024
	// The documented request body limit is 32 MiB including base64 text. Rep
	// stops earlier so one oversized screenshot cannot approach the hard limit.
	MaxImageRequestBytes = 24 * 1024 * 1024
)

// Image is one decoded still PNG or JPEG. Width and Height come from the file
// header so callers can check the documented limits before paying for a request.
type Image struct {
	Format string `json:"format"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Data   []byte `json:"-"`
}

// DecodeImage sniffs a PNG or baseline/progressive JPEG header and enforces the
// documented size limits. Animated PNG (an acTL chunk) is rejected because the
// API rejects it. It never decodes pixel data.
func DecodeImage(data []byte) (Image, error) {
	invalid := func(message string) error { return &Error{Code: CodeInvalidRequest, Message: "image asset " + message} }
	if len(data) == 0 {
		return Image{}, invalid("is empty")
	}
	if len(data) > MaxImageBytes {
		return Image{}, invalid(fmt.Sprintf("has %d bytes, over the %d-byte limit", len(data), MaxImageBytes))
	}
	image := Image{Data: data}
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		image.Format = "png"
		if len(data) < 33 || string(data[12:16]) != "IHDR" {
			return Image{}, invalid("has a damaged PNG header")
		}
		image.Width = int(binary.BigEndian.Uint32(data[16:20]))
		image.Height = int(binary.BigEndian.Uint32(data[20:24]))
		// Walk chunks until image data starts; acTL before IDAT marks an APNG.
		for offset := 8; offset+8 <= len(data); {
			length := int(binary.BigEndian.Uint32(data[offset : offset+4]))
			kind := string(data[offset+4 : offset+8])
			if kind == "acTL" {
				return Image{}, invalid("is an animated PNG, which the API rejects")
			}
			if kind == "IDAT" || kind == "IEND" || length < 0 || length > len(data) {
				break
			}
			offset += 12 + length
		}
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8}):
		image.Format = "jpeg"
		width, height, ok := jpegDimensions(data)
		if !ok {
			return Image{}, invalid("has a damaged JPEG header")
		}
		image.Width, image.Height = width, height
	default:
		return Image{}, invalid("must be a PNG or JPEG file")
	}
	if image.Width < MinImageSide || image.Height < MinImageSide || image.Width > MaxImageSide || image.Height > MaxImageSide {
		return Image{}, invalid(fmt.Sprintf("is %dx%d; each side must be %d to %d pixels", image.Width, image.Height, MinImageSide, MaxImageSide))
	}
	if pixels := image.Width * image.Height; pixels < MinImagePixels || pixels > MaxImagePixels {
		return Image{}, invalid(fmt.Sprintf("has %d pixels; the total must be %d to %d", pixels, MinImagePixels, MaxImagePixels))
	}
	return image, nil
}

// jpegDimensions reads the first start-of-frame marker.
func jpegDimensions(data []byte) (int, int, bool) {
	offset := 2
	for offset+9 <= len(data) {
		if data[offset] != 0xFF {
			return 0, 0, false
		}
		marker := data[offset+1]
		if marker == 0xFF {
			offset++
			continue
		}
		if marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 {
			offset += 2
			continue
		}
		length := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
		if length < 2 {
			return 0, 0, false
		}
		isSOF := marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC
		if isSOF {
			if offset+9 > len(data) {
				return 0, 0, false
			}
			height := int(binary.BigEndian.Uint16(data[offset+5 : offset+7]))
			width := int(binary.BigEndian.Uint16(data[offset+7 : offset+9]))
			return width, height, width > 0 && height > 0
		}
		if marker == 0xDA || marker == 0xD9 {
			return 0, 0, false
		}
		offset += 2 + length
	}
	return 0, 0, false
}

// FitScale returns the largest scale, at most 1, that brings a width x height
// image inside the documented maximum side and pixel limits. Callers render
// screenshots at this scale instead of resampling bytes afterwards.
func FitScale(width, height float64) float64 {
	if width <= 0 || height <= 0 {
		return 1
	}
	scale := math.Min(1, math.Min(float64(MaxImageSide)/width, float64(MaxImageSide)/height))
	if pixels := width * height; pixels*scale*scale > float64(MaxImagePixels) {
		scale = math.Min(scale, math.Sqrt(float64(MaxImagePixels)/pixels))
	}
	// Floor to three decimals so rounding in the renderer cannot exceed a limit.
	return math.Floor(scale*1000) / 1000
}

// TypedQuestion is one System One question of any documented type. Choice uses
// Choices; noul optionally uses TrueFalse definitions; box has no criteria.
type TypedQuestion struct {
	Type         string
	Instructions string
	Choices      map[string]string
	TrueFalse    map[string]string
}

func (question TypedQuestion) MarshalJSON() ([]byte, error) {
	switch question.Type {
	case "choice":
		return json.Marshal(struct {
			Type         string            `json:"type"`
			Instructions string            `json:"instructions"`
			Criteria     map[string]string `json:"criteria"`
		}{question.Type, question.Instructions, question.Choices})
	case "noul":
		if len(question.TrueFalse) == 0 {
			return json.Marshal(struct {
				Type         string `json:"type"`
				Instructions string `json:"instructions"`
			}{question.Type, question.Instructions})
		}
		return json.Marshal(struct {
			Type         string            `json:"type"`
			Instructions string            `json:"instructions"`
			Criteria     map[string]string `json:"criteria"`
		}{question.Type, question.Instructions, question.TrueFalse})
	case "box":
		return json.Marshal(struct {
			Type         string `json:"type"`
			Instructions string `json:"instructions"`
		}{question.Type, question.Instructions})
	}
	return nil, errors.New("unsupported Jev question type")
}

func (question TypedQuestion) validate() error {
	invalid := func(message string) error { return &Error{Code: CodeInvalidRequest, Message: message} }
	if strings.TrimSpace(question.Instructions) == "" {
		return invalid("a Jev question needs instructions")
	}
	switch question.Type {
	case "choice":
		if len(question.Choices) < 2 || len(question.Choices) > MaxChoiceOptions {
			return invalid("a Jev choice question needs 2 to 255 options")
		}
	case "noul":
		if len(question.TrueFalse) != 0 {
			for key := range question.TrueFalse {
				if key != "true" && key != "false" {
					return invalid("Jev noul criteria may only define true and false")
				}
			}
		}
	case "box":
		if len(question.Choices) != 0 || len(question.TrueFalse) != 0 {
			return invalid("a Jev box question takes no criteria")
		}
	default:
		return invalid("unsupported Jev question type")
	}
	return nil
}

// Box is the documented answer geometry: fractions of the image width (left,
// right) and height (top, bottom) with the origin at the top-left corner.
type Box struct {
	Top    float64 `json:"top"`
	Bottom float64 `json:"bottom"`
	Left   float64 `json:"left"`
	Right  float64 `json:"right"`
}

// TypedAnswer carries the fields of whichever answer type the question used.
// Confidence is absent for noul answers and may be absent for box answers.
type TypedAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
	Box           *Box               `json:"box,omitempty"`
}

type Response struct {
	Model   string                 `json:"model"`
	Answers map[string]TypedAnswer `json:"answers"`
	Usage   Usage                  `json:"usage"`
	Timing  []RequestTiming        `json:"timing,omitempty"`
}

// AskRequest is a mixed-type System One request. Asset keys are placeholder
// strings that must appear somewhere in State or a question; the API replaces
// each occurrence with the image and rejects unreferenced assets.
type AskRequest struct {
	State     any
	Questions map[string]TypedQuestion
	Assets    map[string]Image
}

// Asker evaluates typed questions, optionally about images.
type Asker interface {
	Ask(context.Context, AskRequest) (Response, error)
}

func (request AskRequest) needsImages() bool {
	if len(request.Assets) > 0 {
		return true
	}
	for _, question := range request.Questions {
		if question.Type == "box" {
			return true
		}
	}
	return false
}

func askBody(model string, request AskRequest, budget Budget) ([]byte, error) {
	budget = budget.normalized()
	invalid := func(message string) error { return &Error{Code: CodeInvalidRequest, Message: message} }
	if len(request.Questions) == 0 || len(request.Questions) > MaxQuestions {
		return nil, invalid("Jev requires between 1 and 32 questions")
	}
	stateBytes, err := json.Marshal(request.State)
	if err != nil {
		return nil, invalid("invalid Jev state")
	}
	text := string(stateBytes)
	stateTokens := EstimateTokens(stateBytes)
	for _, question := range request.Questions {
		if err := question.validate(); err != nil {
			return nil, err
		}
		questionBytes, err := json.Marshal(question)
		if err != nil {
			return nil, invalid("invalid Jev question")
		}
		text += string(questionBytes)
		if estimated := stateTokens + EstimateTokens(questionBytes); estimated > budget.QuestionTokens {
			return nil, invalid(fmt.Sprintf("Jev state plus a question needs about %d tokens, over the %d-token context budget", estimated, budget.QuestionTokens))
		}
	}
	type wireAsset struct {
		Type   string `json:"type"`
		Format string `json:"format"`
		Data   string `json:"data_base64"`
	}
	assets := map[string]wireAsset{}
	for key, image := range request.Assets {
		if strings.TrimSpace(key) == "" || len(key) > MaxAssetReferenceLen {
			return nil, invalid("a Jev asset reference must be 1 to 1024 non-blank bytes")
		}
		if !strings.Contains(text, key) {
			return nil, invalid("every Jev asset must be referenced in the state or a question")
		}
		checked, err := DecodeImage(image.Data)
		if err != nil {
			return nil, err
		}
		assets[key] = wireAsset{Type: "image", Format: checked.Format, Data: base64.StdEncoding.EncodeToString(image.Data)}
	}
	wire := map[string]any{"model": model, "state": request.State, "questions": request.Questions}
	if len(assets) > 0 {
		wire["assets"] = assets
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, invalid("invalid Jev request")
	}
	// Image tokens have no published estimate yet; text budgets exclude assets.
	textTokens := EstimateTokens([]byte(text))
	limit := MaxRequestBytes
	if len(assets) > 0 {
		limit = MaxImageRequestBytes
	}
	if len(body) > limit || textTokens > budget.RequestTokens {
		return nil, invalid(fmt.Sprintf("Jev request needs about %d text tokens and %d bytes, over the %d-token or %d-byte request budget", textTokens, len(body), budget.RequestTokens, limit))
	}
	return body, nil
}

// Ask evaluates mixed typed questions, including image assets and box
// questions, in one request. It performs no browser action and never retries a
// request that the provider rejected.
func (c *Client) Ask(ctx context.Context, request AskRequest) (Response, error) {
	if c == nil || strings.TrimSpace(c.key) == "" {
		return Response{}, &Error{Code: CodeNotConfigured, Message: "Jev key is not configured; set JEV or run rep jev config --env-file PATH"}
	}
	if !modelName.MatchString(c.model) {
		return Response{}, &Error{Code: CodeInvalidRequest, Message: "invalid Jev model identifier"}
	}
	body, err := askBody(c.model, request, DefaultBudget())
	if err != nil {
		return Response{}, err
	}
	data, timings, err := c.post(ctx, body, request.needsImages())
	if err != nil {
		return Response{}, err
	}
	response, err := decodeResponse(data, request.Questions)
	response.Timing = timings
	return response, err
}

func decodeResponse(data []byte, questions map[string]TypedQuestion) (Response, error) {
	invalid := func(format string, args ...any) error {
		return &Error{Code: CodeInvalidResponse, Status: 200, Message: "Jev returned an invalid typed answer: " + fmt.Sprintf(format, args...)}
	}
	var wire struct {
		Model   string `json:"model"`
		Answers map[string]struct {
			Type          string              `json:"type"`
			Choice        string              `json:"choice"`
			Probabilities map[string]*float64 `json:"probabilities"`
			Confidence    *float64            `json:"confidence"`
			Noul          *float64            `json:"noul"`
			Box           *struct {
				Top    *float64 `json:"top"`
				Bottom *float64 `json:"bottom"`
				Left   *float64 `json:"left"`
				Right  *float64 `json:"right"`
			} `json:"box"`
		} `json:"answers"`
		Usage *struct {
			InputTokens  *int `json:"input_tokens"`
			OutputTokens *int `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &wire) != nil {
		return Response{}, invalid("response is not the documented JSON shape")
	}
	if !modelName.MatchString(wire.Model) {
		return Response{}, invalid("missing or unsafe model identifier")
	}
	if wire.Usage == nil || wire.Usage.InputTokens == nil || wire.Usage.OutputTokens == nil || *wire.Usage.InputTokens < 0 || *wire.Usage.OutputTokens < 0 {
		return Response{}, invalid("missing or negative token usage")
	}
	if len(wire.Answers) != len(questions) {
		return Response{}, invalid("%d answers for %d questions", len(wire.Answers), len(questions))
	}
	result := Response{Model: wire.Model, Answers: make(map[string]TypedAnswer, len(questions)), Usage: Usage{InputTokens: *wire.Usage.InputTokens, OutputTokens: *wire.Usage.OutputTokens}}
	for name, question := range questions {
		answer, exists := wire.Answers[name]
		if !exists {
			return Response{}, invalid("question %q was not answered", name)
		}
		if answer.Type != question.Type {
			return Response{}, invalid("question %q received a %s answer", name, answer.Type)
		}
		typed := TypedAnswer{Type: answer.Type}
		switch question.Type {
		case "choice":
			if answer.Confidence == nil || !isProbability(*answer.Confidence) {
				return Response{}, invalid("question %q has no confidence between 0 and 1", name)
			}
			if len(answer.Probabilities) != len(question.Choices) {
				return Response{}, invalid("question %q has %d probabilities for %d offered options", name, len(answer.Probabilities), len(question.Choices))
			}
			probabilities := make(map[string]float64, len(question.Choices))
			for option := range question.Choices {
				probability, exists := answer.Probabilities[option]
				if !exists || probability == nil || !isProbability(*probability) {
					return Response{}, invalid("question %q lacks a probability between 0 and 1 for an offered option", name)
				}
				probabilities[option] = *probability
			}
			if problem := DistributionProblem(probabilities, answer.Choice); problem != "" {
				return Response{}, invalid("question %q %s", name, problem)
			}
			confidence := *answer.Confidence
			typed.Choice, typed.Probabilities, typed.Confidence = answer.Choice, probabilities, &confidence
		case "noul":
			if answer.Noul == nil || !isProbability(*answer.Noul) {
				return Response{}, invalid("question %q has no noul probability between 0 and 1", name)
			}
			value := *answer.Noul
			typed.Noul = &value
		case "box":
			box := answer.Box
			if box == nil || box.Top == nil || box.Bottom == nil || box.Left == nil || box.Right == nil {
				return Response{}, invalid("question %q has an incomplete box", name)
			}
			edges := Box{Top: *box.Top, Bottom: *box.Bottom, Left: *box.Left, Right: *box.Right}
			for _, edge := range []float64{edges.Top, edges.Bottom, edges.Left, edges.Right} {
				if !isProbability(edge) {
					return Response{}, invalid("question %q has a box edge outside 0..1", name)
				}
			}
			if edges.Left >= edges.Right || edges.Top >= edges.Bottom {
				return Response{}, invalid("question %q has an empty or inverted box", name)
			}
			if answer.Confidence != nil {
				if !isProbability(*answer.Confidence) {
					return Response{}, invalid("question %q has a confidence outside 0..1", name)
				}
				confidence := *answer.Confidence
				typed.Confidence = &confidence
			}
			typed.Box = &edges
		}
		result.Answers[name] = typed
	}
	return result, nil
}
