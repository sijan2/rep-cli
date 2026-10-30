//go:build replab

package commands

import (
	"context"
	"encoding/json"

	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/jev"
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
