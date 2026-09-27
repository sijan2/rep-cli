package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/jevdom"
	"github.com/repplus/rep-cli/internal/jevrpc"
)

func runtimeOptions() []jevdom.Options {
	option := jevdom.DefaultOptions()
	option.TabID, option.Goal, option.Owner = 12, "Find the requested product", "test/task"
	return []jevdom.Options{option}
}

func awaitRuntime(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("runtime condition did not become true")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDecisionRuntimeCoalescesWithoutCancelingAnotherWaiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime := newDecisionRuntime(ctx, nil)
	var calls atomic.Int32
	finish := make(chan struct{})
	runtime.run = func(ctx context.Context, _ jev.Config, _ []jevdom.Options) ([]jevdom.Result, error) {
		calls.Add(1)
		select {
		case <-finish:
			return []jevdom.Result{{Status: "selected"}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	first, stopFirst := context.WithCancel(ctx)
	defer stopFirst()
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	config := jev.Config{APIKey: "private-test-key", Model: jev.Model}
	go func() { _, err := runtime.selectBatch(first, config, runtimeOptions()); firstDone <- err }()
	awaitRuntime(t, func() bool { return calls.Load() == 1 })
	go func() { _, err := runtime.selectBatch(ctx, config, runtimeOptions()); secondDone <- err }()
	awaitRuntime(t, func() bool { runtime.mu.Lock(); defer runtime.mu.Unlock(); return runtime.coalesced == 1 })
	stopFirst()
	if err := <-firstDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("first waiter cancellation: %v", err)
	}
	close(finish)
	if err := <-secondDone; err != nil {
		t.Fatalf("remaining waiter was canceled: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("identical in-flight decisions were evaluated twice")
	}
}

func TestDecisionRuntimeCancelsAbandonedEvaluationAndSeparatesScopes(t *testing.T) {
	runtime := newDecisionRuntime(context.Background(), nil)
	canceled := make(chan struct{}, 2)
	started := make(chan struct{}, 2)
	runtime.run = func(ctx context.Context, _ jev.Config, _ []jevdom.Options) ([]jevdom.Result, error) {
		started <- struct{}{}
		<-ctx.Done()
		canceled <- struct{}{}
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	options := runtimeOptions()
	other := runtimeOptions()
	other[0].Owner = "test/other"
	var done atomic.Int32
	for _, batch := range [][]jevdom.Options{options, other} {
		go func(batch []jevdom.Options) {
			_, _ = runtime.selectBatch(ctx, jev.Config{APIKey: "private"}, batch)
			done.Add(1)
		}(batch)
	}
	<-started
	<-started
	cancel()
	awaitRuntime(t, func() bool { return done.Load() == 2 && len(canceled) == 2 })
	if runtime.coalesced != 0 {
		t.Fatal("different task scopes coalesced")
	}
}

func TestDecisionHostConsumesPrivateMethodsAndPreservesCLIConfiguration(t *testing.T) {
	runtime := newDecisionRuntime(context.Background(), nil)
	runtime.loadConfig = func() (jev.Config, error) { t.Fatal("CLI credentials lost precedence"); return jev.Config{}, nil }
	var received bool
	runtime.run = func(_ context.Context, config jev.Config, options []jevdom.Options) ([]jevdom.Result, error) {
		received = config.APIKey == "private-unix-only-key" && options[0].Owner == "test/task"
		return []jevdom.Result{{Status: "selected"}}, nil
	}
	input := jevrpc.SelectRequest{Version: 1, Credentials: &jevrpc.Credentials{APIKey: "private-unix-only-key"}, Options: runtimeOptions()}
	data, _ := json.Marshal(input)
	response, handled := runtime.handle(context.Background(), RPCRequest{ID: "selection", Method: "jev.select", Params: data})
	if !handled || response.Error != nil || !received {
		t.Fatalf("local selection failed: %+v", response.Error)
	}
	encoded, _ := json.Marshal(response)
	if strings.Contains(string(encoded), "private-unix-only-key") {
		t.Fatal("credential escaped into decision output")
	}
	for _, method := range []string{"jev.unknown", "jev.select"} {
		response, handled = runtime.handle(context.Background(), RPCRequest{ID: "bad", Method: method, Params: json.RawMessage(`{"credentials":{"api_key":"private-unix-only-key"},"unknown":true}`)})
		if !handled || response.Error == nil || strings.Contains(response.Error.Message, "private-unix-only-key") {
			t.Fatal("invalid private request could cross the browser boundary")
		}
	}
	if _, handled := runtime.handle(context.Background(), RPCRequest{Method: "browser.tabs"}); handled {
		t.Fatal("runtime intercepted a browser operation")
	}
}

func TestDecisionClientPoolKeysCredentialsAndModel(t *testing.T) {
	runtime := newDecisionRuntime(context.Background(), nil)
	config := jev.Config{APIKey: "first", Model: "jev-1.13.0"}
	first := runtime.client(config)
	if runtime.client(config) != first {
		t.Fatal("provider client was not retained")
	}
	config.APIKey = "second"
	if runtime.client(config) == first {
		t.Fatal("changed credential reused the old configured client")
	}
	config.APIKey, config.Model = "first", "jev-latest"
	if runtime.client(config) == first {
		t.Fatal("changed model reused the old configured client")
	}
}

func TestDecisionSocketDisconnectCancelsLastWaiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime := newDecisionRuntime(ctx, nil)
	started, stopped := make(chan struct{}), make(chan struct{})
	runtime.run = func(ctx context.Context, _ jev.Config, _ []jevdom.Options) ([]jevdom.Result, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}
	server := &bridgeServer{ctx: ctx, decisions: runtime}
	peer, local := net.Pipe()
	defer peer.Close()
	go server.handleConnection(local)
	data, _ := json.Marshal(jevrpc.SelectRequest{Version: 1, Credentials: &jevrpc.Credentials{APIKey: "test"}, Options: runtimeOptions()})
	if err := json.NewEncoder(peer).Encode(RPCRequest{ID: "disconnect", Method: "jev.select", Params: data}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("selection did not start")
	}
	_ = peer.Close()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("disconnected CLI left provider work running")
	}
}

func TestNativeBrowserCallerRoutesResponsesWithoutHostSocket(t *testing.T) {
	caller := nativeBrowserCaller{send: func(value any) error {
		command := value.(map[string]any)
		routeRPCResponse(&Message{ID: command["id"].(string), Result: json.RawMessage(`{"fresh":true}`)})
		return nil
	}}
	var result struct{ Fresh bool }
	if err := caller.Call(context.Background(), "browser.validate", map[string]any{"tab_id": 12}, &result); err != nil || !result.Fresh {
		t.Fatalf("native response routing failed: %v", err)
	}
}

func TestHostLocalConfigurationSuppliesOmittedModel(t *testing.T) {
	runtime := newDecisionRuntime(context.Background(), nil)
	runtime.loadConfig = func() (jev.Config, error) { return jev.Config{APIKey: "host-only", Model: "jev-1.13.0"}, nil }
	runtime.run = func(_ context.Context, config jev.Config, options []jevdom.Options) ([]jevdom.Result, error) {
		if config.APIKey != "host-only" || config.Model != "jev-1.13.0" || options[0].Model != "jev-1.13.0" || options[0].Confidence != 0.8 {
			t.Fatal("host defaults or confidence lost")
		}
		return []jevdom.Result{{Status: "no_match"}}, nil
	}
	response, handled := runtime.handle(context.Background(), RPCRequest{ID: "test", Method: "jev.select", Params: json.RawMessage(`{"version":1,"options":[{"tab_id":12,"owner":"workspace/task","goal":"Save"}]}`)})
	if !handled || response.Error != nil {
		t.Fatalf("host default failed: %+v", response.Error)
	}
}

func stepRPC(t *testing.T, runtime *decisionRuntime, params any) RPCResponse {
	t.Helper()
	encoded, _ := json.Marshal(params)
	response, handled := runtime.handle(context.Background(), RPCRequest{ID: "step-1", Method: "jev.step", Params: encoded})
	if !handled {
		t.Fatal("jev.step was forwarded to the browser")
	}
	return response
}

func TestHostStepInheritsModelAndRequiresOwner(t *testing.T) {
	runtime := newDecisionRuntime(context.Background(), nil)
	runtime.loadConfig = func() (jev.Config, error) { return jev.Config{APIKey: "host-key", Model: "jev-1.13.0"}, nil }
	var seen jev.Config
	var seenOptions jevdom.StepOptions
	runtime.step = func(_ context.Context, config jev.Config, options jevdom.StepOptions) (jevdom.StepDecision, error) {
		seen, seenOptions = config, options
		return jevdom.StepDecision{Status: "selected", Operation: jevdom.OpDone}, nil
	}
	response := stepRPC(t, runtime, map[string]any{"version": jevrpc.Version, "options": map[string]any{"tab_id": 3, "goal": "Finish checkout", "owner": "work/task", "value_keys": []string{"email"}}})
	if response.Error != nil {
		t.Fatalf("step failed: %+v", response.Error)
	}
	if seen.APIKey != "host-key" || seen.Model != "jev-1.13.0" || seenOptions.Model != "jev-1.13.0" || seenOptions.Kind != "controls" || len(seenOptions.ValueKeys) != 1 {
		t.Fatalf("host did not apply its configuration: %+v %+v", seen, seenOptions)
	}
	var decision jevdom.StepDecision
	if json.Unmarshal(response.Result, &decision) != nil || decision.Operation != jevdom.OpDone {
		t.Fatalf("unexpected result %s", response.Result)
	}
	for _, invalid := range []map[string]any{
		{"version": jevrpc.Version, "options": map[string]any{"tab_id": 3, "goal": "Finish"}},
		{"version": 99, "options": map[string]any{"tab_id": 3, "goal": "Finish", "owner": "a/b"}},
		{"version": jevrpc.Version, "options": map[string]any{"tab_id": 3, "goal": "Finish", "owner": "a/b", "unknown": true}},
		{"version": jevrpc.Version, "options": map[string]any{"tab_id": 3, "goal": "Finish", "owner": "a/b", "value_keys": []string{"a", "A"}}},
	} {
		if response := stepRPC(t, runtime, invalid); response.Error == nil {
			t.Fatalf("accepted invalid step request %v", invalid)
		}
	}
	var capabilities jevrpc.Capabilities
	response, _ = runtime.handle(context.Background(), RPCRequest{ID: "c", Method: "jev.capabilities"})
	if json.Unmarshal(response.Result, &capabilities) != nil || !capabilities.StepDecision {
		t.Fatal("host does not advertise step decisions")
	}
}

func TestHostStepSharesBoundedProviderSlots(t *testing.T) {
	runtime := newDecisionRuntime(context.Background(), nil)
	runtime.loadConfig = func() (jev.Config, error) { return jev.Config{APIKey: "k", Model: jev.Model}, nil }
	release := make(chan struct{})
	var active, peak atomic.Int32
	runtime.step = func(context.Context, jev.Config, jevdom.StepOptions) (jevdom.StepDecision, error) {
		now := active.Add(1)
		for {
			old := peak.Load()
			if now <= old || peak.CompareAndSwap(old, now) {
				break
			}
		}
		<-release
		active.Add(-1)
		return jevdom.StepDecision{Status: "selected"}, nil
	}
	done := make(chan struct{})
	for i := 0; i < 6; i++ {
		go func() {
			stepRPC(t, runtime, map[string]any{"version": jevrpc.Version, "options": map[string]any{"tab_id": 3, "goal": "Go", "owner": "a/b"}})
			done <- struct{}{}
		}()
	}
	awaitRuntime(t, func() bool { return active.Load() == int32(cap(runtime.slots)) })
	close(release)
	for i := 0; i < 6; i++ {
		<-done
	}
	if peak.Load() > int32(cap(runtime.slots)) {
		t.Fatalf("steps exceeded provider slots: %d", peak.Load())
	}
}
