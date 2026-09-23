package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/jevdom"
)

type jevCommandBrowser struct {
	t       *testing.T
	methods []string
}

func (browser *jevCommandBrowser) Call(_ context.Context, method string, params any, out any) error {
	browser.t.Helper()
	if method != "browser.cdp" {
		browser.t.Fatalf("unexpected browser method: %s", method)
	}
	values := params.(map[string]any)
	if values["tab_id"] != 12 || values["keep_attached"] != false {
		browser.t.Fatal("lookup changed browser ownership")
	}
	cdpMethod := values["method"].(string)
	browser.methods = append(browser.methods, cdpMethod)
	var result string
	switch cdpMethod {
	case "Page.getFrameTree":
		result = `{"result":{"frameTree":{"frame":{"id":"frame-one","loaderId":"loader-one","url":"https://canvas.example.edu/courses/42"}}}}`
	case "Accessibility.getFullAXTree":
		result = `{"result":{"nodes":[{"nodeId":"1","backendDOMNodeId":42,"frameId":"frame-one","ignored":false,"role":{"value":"link"},"name":{"value":"Modules"}}]}}`
	default:
		browser.t.Fatalf("lookup invoked a browser action: %s", cdpMethod)
	}
	return json.Unmarshal([]byte(result), out)
}

type jevCommandEvaluator struct {
	t     *testing.T
	calls int
}

func (evaluator *jevCommandEvaluator) Evaluate(_ context.Context, state any, questions map[string]jev.Question) (jev.Evaluation, error) {
	evaluator.t.Helper()
	evaluator.calls++
	encoded, err := json.Marshal(state)
	if err != nil {
		evaluator.t.Fatal(err)
	}
	for _, private := range []string{"unit-test-command-key", "canvas.example.edu", "frame-one", "backend_dom_node_id", "loader-one"} {
		if strings.Contains(string(encoded), private) {
			evaluator.t.Fatalf("private field reached Jev: %s", private)
		}
	}
	probabilities := make(map[string]float64)
	selected := ""
	for id := range questions["selection"].Criteria {
		probabilities[id] = 0
		if id != "none" {
			selected = id
		}
	}
	if selected == "" {
		evaluator.t.Fatal("no observed candidate was supplied")
	}
	probabilities[selected] = 1
	return jev.Evaluation{Model: "jev-1.13.0", Usage: jev.Usage{InputTokens: 20, OutputTokens: 10}, Answers: map[string]jev.ChoiceAnswer{
		"selection": {Type: "choice", Choice: selected, Probabilities: probabilities, Confidence: 0.95},
	}}, nil
}

func TestJevSelectValidatesFlagsBeforeConfigOrBrowser(t *testing.T) {
	cases := [][]string{
		{"--goal", "Modules"},
		{"--tab", "12"},
		{"--tab", "-1", "--goal", "Modules"},
		{"--tab", "12", "--goal", "Modules", "--kind", "click"},
		{"--tab", "12", "--goal", "Modules", "--confidence", "NaN"},
		{"--tab", "12", "--goal", "Modules", "--limit", "481"},
		{"--tab", "12", "--goal", "Modules", "--origin", "https://canvas.example.edu/private"},
		{"--tab", "12", "--goal", "Modules", "--browser", "unsupported"},
		{"--tab", "12", "--goal", "Modules", "--execute"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			command := newBrowserSelectCommand(jevDOMDependencies{loadConfig: func() (jev.Config, error) { t.Fatal("invalid arguments reached credentials"); return jev.Config{}, nil }})
			command.SetArgs(args)
			command.SetOut(&bytes.Buffer{})
			command.SetErr(&bytes.Buffer{})
			if err := command.Execute(); err == nil {
				t.Fatal("invalid lookup arguments were accepted")
			}
		})
	}
}

func TestJevSelectConfigFailureDoesNotReadBrowser(t *testing.T) {
	command := newBrowserSelectCommand(jevDOMDependencies{
		loadConfig: func() (jev.Config, error) { return jev.Config{}, errors.New("Jev key unavailable") },
		browser: func(context.Context, string) (jevdom.Browser, error) {
			t.Fatal("configuration failure reached browser")
			return nil, nil
		},
	})
	command.SetArgs([]string{"--tab", "12", "--goal", "Modules"})
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	if err := command.Execute(); err == nil {
		t.Fatal("configuration error was ignored")
	}
}

func TestJevSelectReturnsObservedHandleWithoutActionsOrCredentialExposure(t *testing.T) {
	for _, noCache := range []bool{false, true} {
		t.Run(map[bool]string{false: "cache enabled", true: "cache bypass"}[noCache], func(t *testing.T) {
			browser := &jevCommandBrowser{t: t}
			evaluator := &jevCommandEvaluator{t: t}
			cacheCalls := 0
			command := newBrowserSelectCommand(jevDOMDependencies{
				loadConfig: func() (jev.Config, error) {
					return jev.Config{APIKey: "unit-test-command-key", Model: "jev-1.13.0"}, nil
				},
				browser: func(_ context.Context, name string) (jevdom.Browser, error) {
					if name != "arc" {
						t.Fatalf("unexpected browser: %s", name)
					}
					return browser, nil
				},
				evaluator: func(config jev.Config) jevdom.Evaluator {
					if config.APIKey != "unit-test-command-key" || config.Model != "jev-1.13.0" {
						t.Fatal("evaluator did not receive local configuration")
					}
					return evaluator
				},
				cache: func() *jevdom.Cache { cacheCalls++; return jevdom.NewCache(t.TempDir()) },
			})
			args := []string{"--tab", "12", "--goal", "Find Modules", "--origin", "https://canvas.example.edu"}
			if noCache {
				args = append(args, "--no-cache")
			}
			var stdout, stderr bytes.Buffer
			command.SetArgs(args)
			command.SetOut(&stdout)
			command.SetErr(&stderr)
			if err := command.Execute(); err != nil {
				t.Fatalf("lookup failed: %v", err)
			}
			if evaluator.calls != 1 || len(browser.methods) != 6 {
				t.Fatalf("expected one decision and two verified snapshots: evaluations=%d reads=%d", evaluator.calls, len(browser.methods))
			}
			if (noCache && cacheCalls != 0) || (!noCache && cacheCalls != 1) {
				t.Fatal("cache bypass was not honored")
			}
			if strings.Contains(stdout.String()+stderr.String(), "unit-test-command-key") {
				t.Fatal("credential appeared in command output")
			}
			var payload json.RawMessage = stdout.Bytes()
			var envelope struct {
				Data json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(payload, &envelope); err != nil {
				t.Fatal(err)
			}
			if len(envelope.Data) > 0 {
				payload = envelope.Data
			}
			var result jevdom.Result
			if err := json.Unmarshal(payload, &result); err != nil {
				t.Fatal(err)
			}
			if result.Status != "selected" || result.NeedsReview || result.Selected == nil || result.Selected.BackendDOMNodeID != 42 || result.Selected.FrameID != "frame-one" {
				t.Fatalf("invalid observed selection: %+v", result)
			}
		})
	}
}
