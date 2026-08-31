package cmd

import (
	"testing"

	"github.com/repplus/rep-cli/internal/bridge"
)

func TestReplacementBridgeCandidatesRejectUnrelatedRegistries(t *testing.T) {
	previous := bridge.Registry{
		PID:          100,
		ParentPID:    42,
		Browser:      "arc",
		BrowserLabel: "Arc",
		ExtensionID:  "abcdefghijklmnopabcdefghijklmnop",
		UserAgent:    "Rep Browser/1.0",
		StartedAt:    "2026-08-25T12:00:00Z",
	}
	registries := []bridge.Registry{
		{PID: 201, ParentPID: 42, Browser: "arc", BrowserLabel: "Arc", ExtensionID: "other-extension", UserAgent: previous.UserAgent, StartedAt: "2026-08-25T12:00:02Z"},
		{PID: 202, ParentPID: 99, Browser: "arc", BrowserLabel: "Arc", ExtensionID: previous.ExtensionID, UserAgent: previous.UserAgent, StartedAt: "2026-08-25T12:00:03Z"},
		{PID: 203, ParentPID: 42, Browser: "chrome", BrowserLabel: "Google Chrome", ExtensionID: previous.ExtensionID, UserAgent: previous.UserAgent, StartedAt: "2026-08-25T12:00:04Z"},
		{PID: 204, ParentPID: 42, Browser: "arc", BrowserLabel: "Arc", ExtensionID: previous.ExtensionID, UserAgent: "different profile agent", StartedAt: "2026-08-25T12:00:05Z"},
		previous,
		{PID: 205, ParentPID: 42, Browser: "ARC", BrowserLabel: "arc", ExtensionID: previous.ExtensionID, UserAgent: previous.UserAgent, StartedAt: "2026-08-25T12:00:06Z"},
	}

	candidates := replacementBridgeCandidates(previous, registries)
	if len(candidates) != 1 || candidates[0].PID != 205 {
		t.Fatalf("matched unrelated replacement registries: %+v", candidates)
	}
}

func TestReplacementBridgeCandidatesRequireAvailableStableIdentity(t *testing.T) {
	previous := bridge.Registry{
		PID:          100,
		ParentPID:    42,
		Browser:      "arc",
		BrowserLabel: "Arc",
		ExtensionID:  "abcdefghijklmnopabcdefghijklmnop",
		UserAgent:    "Rep Browser/1.0",
		StartedAt:    "old",
	}
	tests := []struct {
		name      string
		candidate bridge.Registry
	}{
		{name: "missing extension", candidate: bridge.Registry{PID: 200, ParentPID: 42, Browser: "arc", BrowserLabel: "Arc", UserAgent: previous.UserAgent, StartedAt: "new"}},
		{name: "missing parent", candidate: bridge.Registry{PID: 200, Browser: "arc", BrowserLabel: "Arc", ExtensionID: previous.ExtensionID, UserAgent: previous.UserAgent, StartedAt: "new"}},
		{name: "missing browser label", candidate: bridge.Registry{PID: 200, ParentPID: 42, Browser: "arc", ExtensionID: previous.ExtensionID, UserAgent: previous.UserAgent, StartedAt: "new"}},
		{name: "missing user agent", candidate: bridge.Registry{PID: 200, ParentPID: 42, Browser: "arc", BrowserLabel: "Arc", ExtensionID: previous.ExtensionID, StartedAt: "new"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if matchesReplacementBridge(previous, test.candidate) {
				t.Fatalf("candidate missing stable identity matched: %+v", test.candidate)
			}
		})
	}
}
