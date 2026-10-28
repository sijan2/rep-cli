package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestJevNativeRejectsPayloadsBeforeNetwork(t *testing.T) {
	for _, params := range []string{`{"url":"https://example.com","headers":{"authorization":"private"}}`, `{"url":"https://example.com","body":"private"}`, `{"url":"https://example.com"} {}`} {
		responses := make(chan map[string]interface{}, 1)
		if !dispatchJev(context.Background(), &Message{Action: "jev_classify", ID: "test-1", Params: json.RawMessage(params)}, func(v interface{}) error { responses <- v.(map[string]interface{}); return nil }) {
			t.Fatal("Jev action not handled")
		}
		response := <-responses
		if response["id"] != "test-1" || response["success"] != false || response["error"] == nil {
			t.Fatalf("unsafe response: %+v", response)
		}
	}
}

func TestJevNativeConcurrencyIsBounded(t *testing.T) {
	for i := 0; i < cap(jevSlots); i++ {
		jevSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(jevSlots); i++ {
			<-jevSlots
		}
	}()
	responses := make(chan map[string]interface{}, 1)
	dispatchJev(context.Background(), &Message{Action: "jev_classify", ID: "busy-test", Params: json.RawMessage(`{"url":"https://example.com"}`)}, func(v interface{}) error { responses <- v.(map[string]interface{}); return nil })
	select {
	case response := <-responses:
		if response["success"] != false || response["id"] != "busy-test" {
			t.Fatalf("invalid busy response: %+v", response)
		}
	case <-time.After(time.Second):
		t.Fatal("native read loop blocked on Jev concurrency")
	}
	if dispatchJev(context.Background(), &Message{Action: "ping"}, nil) {
		t.Fatal("intercepted unrelated native message")
	}
}

func TestJevNativeEvaluationDoesNotBlockCaptureDispatch(t *testing.T) {
	t.Setenv("JEV", "unit-test-key")
	t.Setenv("JEV_MODEL", "jev-latest")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // No network access; the async completion writer still blocks.
	allowWrite := make(chan struct{})
	written := make(chan struct{})
	returned := make(chan struct{})
	go func() {
		dispatchJev(ctx, &Message{Action: "jev_classify", ID: "async-test", Params: json.RawMessage(`{"url":"https://example.com"}`)}, func(v interface{}) error {
			<-allowWrite
			close(written)
			return nil
		})
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		close(allowWrite)
		t.Fatal("Jev completion blocked the capture dispatcher")
	}
	resetHostState()
	response, _ := handleMessage(&Message{Action: "ping"})
	if response["action"] != "pong" {
		t.Fatal("ordinary native action was not processed")
	}
	close(allowWrite)
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("canceled Jev call did not finish")
	}
}
