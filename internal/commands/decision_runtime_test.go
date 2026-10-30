package commands

import (
	"context"

	"errors"

	"testing"

	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/jevdom"
	"github.com/repplus/rep-cli/internal/jevrpc"
)

type decisionBrowserFunc func(context.Context, string, any, any) error

func (f decisionBrowserFunc) Call(ctx context.Context, method string, params, out any) error {
	return f(ctx, method, params, out)
}

func TestHostCapabilityNegotiationPrecedesPrivateCredentials(t *testing.T) {
	var calls []string
	browser := decisionBrowserFunc(func(_ context.Context, method string, params, _ any) error {
		calls = append(calls, method)
		if method != "jev.capabilities" || params != nil {
			t.Fatal("credential-bearing request reached an unverified host")
		}
		return errors.New("old host")
	})
	_, err := hostSelectBatch(context.Background(), browser, jev.Config{APIKey: "must-stay-local"}, []jevdom.Options{{TabID: 12, Goal: "Continue"}})
	if err == nil || len(calls) != 1 {
		t.Fatal("older host did not fail before credentials")
	}
}

func TestHostSelectionBatchRetainsGoalOrderAndUsesOneDecisionRPC(t *testing.T) {
	var decisions int
	browser := decisionBrowserFunc(func(_ context.Context, method string, params, out any) error {
		if method == "jev.capabilities" {
			*out.(*jevrpc.Capabilities) = jevrpc.Capabilities{Version: 1, PersistentSelection: true, BatchSelection: true, MaxBatch: 32}
			return nil
		}
		if method != "jev.select_batch" {
			t.Fatalf("unexpected request: %s", method)
		}
		request := params.(jevrpc.SelectRequest)
		if request.Credentials.APIKey != "unix-only" || request.Options[0].Goal != "Name" || request.Options[1].Goal != "Email" {
			t.Fatal("batch or local configuration was changed")
		}
		decisions++
		*out.(*jevrpc.BatchResult) = jevrpc.BatchResult{Results: []jevdom.Result{{Status: "selected", Confidence: 0.91}, {Status: "no_match", Confidence: 0.92}}}
		return nil
	})
	results, err := hostSelectBatch(context.Background(), browser, jev.Config{APIKey: "unix-only"}, []jevdom.Options{{Goal: "Name"}, {Goal: "Email"}})
	if err != nil || decisions != 1 || len(results) != 2 || results[1].Status != "no_match" {
		t.Fatalf("invalid batch: %v", err)
	}
}
