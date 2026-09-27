package jevdom

import (
	"context"
	"strings"
	"testing"
)

func scopedProductPage(product, outside string) fakePage {
	value := page(0)
	value.frames[0].nodes = append(value.frames[0].nodes, node(2, "row", "", "1000000"), node(3, "StaticText", product, "2"), node(4, "button", "Buy", "2"), node(5, "button", outside, "1000000"))
	return value
}

func TestExplicitSubtreeKeepsContextDependenciesWithoutUnrelatedCandidates(t *testing.T) {
	opt := options()
	opt.FrameID = "frame-main"
	opt.ScopeBackendDOMNodeID = 4
	a, err := captureOptions(context.Background(), browserFor(scopedProductPage("Widget Basic $10", "Outside")), opt)
	if err != nil {
		t.Fatal(err)
	}
	b, err := captureOptions(context.Background(), browserFor(scopedProductPage("Widget Basic $10", "Outside changed")), opt)
	if err != nil {
		t.Fatal(err)
	}
	c, err := captureOptions(context.Background(), browserFor(scopedProductPage("Widget Pro $20", "Outside")), opt)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Candidates) != 1 || a.Candidates[0].BackendDOMNodeID != 4 || len(a.Candidates[0].ContextRelations) == 0 || !strings.Contains(a.Candidates[0].ContextRelations[0].Name, "Widget Basic $10") {
		t.Fatalf("scope lost contextual sibling: %+v", a.Candidates)
	}
	if a.Fingerprint != b.Fingerprint {
		t.Fatal("unrelated candidate invalidated an explicit closed subtree")
	}
	if a.Fingerprint == c.Fingerprint {
		t.Fatal("relevant sibling entity changed without invalidation")
	}
	opt.ScopeBackendDOMNodeID = 999
	if _, err = captureOptions(context.Background(), browserFor(scopedProductPage("Widget", "Outside")), opt); err == nil {
		t.Fatal("missing subtree root accepted")
	}
}

func TestExplicitFrameScopeRejectsUnknownOrAmbiguousFrame(t *testing.T) {
	value := page(1)
	value.frames = append(value.frames, fakeFrame{id: "child", loader: "child-loader", url: value.frames[0].url, nodes: []map[string]any{node(9, "button", "Child", "")}})
	opt := options()
	opt.FrameURL = value.frames[0].url
	if _, err := captureOptions(context.Background(), browserFor(value), opt); err == nil {
		t.Fatal("ambiguous URL frame accepted")
	}
	opt.FrameID = "child"
	snapshot, err := captureOptions(context.Background(), browserFor(value), opt)
	if err != nil || len(snapshot.Candidates) != 1 || snapshot.Candidates[0].Name != "Child" {
		t.Fatalf("scope=%+v err=%v", snapshot, err)
	}
	opt.FrameID = "missing"
	if _, err = captureOptions(context.Background(), browserFor(value), opt); err == nil {
		t.Fatal("missing frame accepted")
	}
}
