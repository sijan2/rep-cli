package jevdom

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/repplus/rep-cli/internal/bridge"
)

type compactBrowser struct {
	t                 *testing.T
	snapshot          Snapshot
	methods           []string
	failure           error
	validationChanged bool
	legacy            *fakeBrowser
}

func (b *compactBrowser) Call(ctx context.Context, method string, params any, out any) error {
	b.methods = append(b.methods, method)
	if method == "browser.cdp" && b.legacy != nil {
		return b.legacy.Call(ctx, method, params, out)
	}
	if b.failure != nil {
		return b.failure
	}
	p := params.(map[string]any)
	if p["owner"] != "test-owner" || p["lease_id"] != "test-lease" {
		b.t.Fatal("observation lost owner/lease identity")
	}
	var result any
	switch method {
	case "browser.observe":
		result = b.snapshot
	case "browser.validate":
		if p["generation"] != b.snapshot.Generation || p["fingerprint"] != b.snapshot.Fingerprint {
			b.t.Fatal("validation lost observation identity")
		}
		snapshot := b.snapshot
		if b.validationChanged {
			snapshot.Generation = "new-generation"
		}
		result = map[string]any{"fresh": true, "snapshot": snapshot}
	default:
		return errors.New("unexpected method")
	}
	data, _ := json.Marshal(result)
	return json.Unmarshal(data, out)
}

func compactOptions() Options {
	o := options()
	o.Owner = "test-owner"
	o.LeaseID = "test-lease"
	return o
}

func TestHostOptionsDecodeDefaultsWithoutWeakeningExplicitConfidence(t *testing.T) {
	var option Options
	if err := json.Unmarshal([]byte(`{"tab_id":12,"goal":"Find control"}`), &option); err != nil {
		t.Fatal(err)
	}
	if option.Confidence != 0.8 || option.Kind != "controls" || option.Limit != 240 {
		t.Fatalf("JSON request lost safe defaults: %+v", option)
	}
	if err := json.Unmarshal([]byte(`{"tab_id":12,"goal":"Find control","confidence":0}`), &option); err != nil || option.Confidence != 0 {
		t.Fatal("explicit confidence zero was replaced")
	}
	if err := json.Unmarshal([]byte(`{"goal":"Find control"}`), &option); err != nil {
		t.Fatal(err)
	}
	if _, err := option.Validate(); err == nil {
		t.Fatal("omitted tab became tab zero")
	}
	if err := json.Unmarshal([]byte(`{"tab_id":12,"goal":"Find control","unexpected":true}`), &option); err == nil {
		t.Fatal("custom defaults bypassed unknown field rejection")
	}
}

func TestCompactObservationReturnsVersionedBindingAndRevalidatesGeneration(t *testing.T) {
	snapshot, err := Capture(context.Background(), browserFor(page(2)), 12, "controls", "")
	if err != nil {
		t.Fatal(err)
	}
	browser := &compactBrowser{t: t, snapshot: snapshot}
	selector := Selector{Browser: browser, Evaluator: &fakeEvaluator{target: "Item 0"}}
	result, err := selector.Select(context.Background(), compactOptions())
	if err != nil || result.Status != "selected" || result.Binding == nil || result.Binding.ObservationMode != "compact" || len(browser.methods) != 2 {
		t.Fatalf("result=%+v err=%v methods=%v", result, err, browser.methods)
	}
	browser.validationChanged = true
	fresh, err := selector.Validate(context.Background(), compactOptions(), *result.Binding)
	if err != nil || fresh {
		t.Fatalf("changed generation accepted: fresh=%v err=%v", fresh, err)
	}
}

func TestCompactFallbackOnlyForUnsupportedMethod(t *testing.T) {
	for _, code := range []string{"unknown_method", "observation_incomplete"} {
		b := &compactBrowser{t: t, failure: &bridge.RPCError{Code: code, Message: "test"}, legacy: browserFor(page(2))}
		result, err := (Selector{Browser: b, Evaluator: &fakeEvaluator{target: "Item 0"}}).Select(context.Background(), compactOptions())
		if code == "unknown_method" {
			if err != nil || result.Status != "selected" || result.Timing.ObservationMode != "legacy" {
				t.Fatalf("compatibility fallback failed: %+v %v", result, err)
			}
		} else if err == nil {
			t.Fatal("failed compact observation silently became a legacy read")
		}
	}
}

func TestCompactMalformedProjectionNeverReachesProvider(t *testing.T) {
	snapshot, err := Capture(context.Background(), browserFor(page(1)), 12, "controls", "")
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Candidates[0].DocumentGeneration = ""
	e := &fakeEvaluator{}
	_, err = (Selector{Browser: &compactBrowser{t: t, snapshot: snapshot}, Evaluator: e}).Select(context.Background(), compactOptions())
	if err == nil || len(e.calls) > 0 {
		t.Fatal("unversioned compact node reached provider")
	}
}

func TestCacheHitUsesOneFreshObservationAndExecutionStillValidates(t *testing.T) {
	snapshot, err := Capture(context.Background(), browserFor(page(1)), 12, "controls", "")
	if err != nil {
		t.Fatal(err)
	}
	browser := &compactBrowser{t: t, snapshot: snapshot}
	e := &fakeEvaluator{target: "Item 0"}
	selector := Selector{Browser: browser, Evaluator: e, Cache: NewCache(t.TempDir())}
	if _, err = selector.Select(context.Background(), compactOptions()); err != nil {
		t.Fatal(err)
	}
	browser.methods = nil
	result, err := selector.Select(context.Background(), compactOptions())
	if err != nil || !result.CacheHit || len(e.calls) != 1 || len(browser.methods) != 1 || browser.methods[0] != "browser.observe" || result.Timing.Requests != 0 || result.Timing.ValidationMS != 0 {
		t.Fatalf("cache result=%+v calls=%v err=%v", result, browser.methods, err)
	}
	browser.validationChanged = true
	fresh, err := selector.Validate(context.Background(), compactOptions(), *result.Binding)
	if err != nil || fresh {
		t.Fatalf("cache binding bypassed execution validation: fresh=%v err=%v", fresh, err)
	}
}
