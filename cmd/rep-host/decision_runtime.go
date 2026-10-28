package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/browserrpc"
	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/jevdom"
	"github.com/repplus/rep-cli/internal/jevrpc"
)

const maxDecisionClients = 16
const maxDecisionFlights = 64
const maxDecisionPayload = 128 * 1024

type decisionFlight struct {
	done     chan struct{}
	cancel   context.CancelFunc
	waiters  int
	finished bool
	results  []jevdom.Result
	err      error
}

type pooledDecisionClient struct {
	client *jev.Client
	used   time.Time
}

// A runtime belongs to one native host/browser lifetime. It never holds capture
// locks while waiting for the provider, the browser, or another decision.
type decisionRuntime struct {
	ctx        context.Context
	browser    browserrpc.Caller
	mu         sync.Mutex
	flights    map[string]*decisionFlight
	clients    map[string]pooledDecisionClient
	slots      chan struct{}
	cacheRoot  string
	loadConfig func() (jev.Config, error)
	run        func(context.Context, jev.Config, []jevdom.Options) ([]jevdom.Result, error)
	step       func(context.Context, jev.Config, jevdom.StepOptions) (jevdom.StepDecision, error)
	started    uint64
	coalesced  uint64
	completed  uint64
}

func newDecisionRuntime(ctx context.Context, browser browserrpc.Caller) *decisionRuntime {
	runtime := &decisionRuntime{ctx: ctx, browser: browser, flights: map[string]*decisionFlight{}, clients: map[string]pooledDecisionClient{}, slots: make(chan struct{}, 4), loadConfig: jev.LoadConfig}
	if cache := jevdom.DefaultCache(); cache != nil {
		runtime.cacheRoot = filepath.Join(cache.Directory, "host-v1")
	}
	runtime.run = runtime.evaluate
	runtime.step = func(ctx context.Context, config jev.Config, options jevdom.StepOptions) (jevdom.StepDecision, error) {
		return jevdom.Selector{Browser: runtime.browser, Evaluator: runtime.client(config)}.Step(ctx, options)
	}
	return runtime
}

func decisionDigest(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (runtime *decisionRuntime) client(config jev.Config) *jev.Client {
	key := decisionDigest([]string{config.APIKey, config.Model})
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if saved, ok := runtime.clients[key]; ok {
		saved.used = time.Now()
		runtime.clients[key] = saved
		return saved.client
	}
	if len(runtime.clients) >= maxDecisionClients {
		oldest := ""
		var at time.Time
		for candidate, saved := range runtime.clients {
			if oldest == "" || saved.used.Before(at) {
				oldest, at = candidate, saved.used
			}
		}
		delete(runtime.clients, oldest)
	}
	client := jev.NewClient(config)
	runtime.clients[key] = pooledDecisionClient{client: client, used: time.Now()}
	return client
}

func (runtime *decisionRuntime) evaluate(ctx context.Context, config jev.Config, options []jevdom.Options) ([]jevdom.Result, error) {
	selector := jevdom.Selector{Browser: runtime.browser, Evaluator: runtime.client(config)}
	if runtime.cacheRoot != "" {
		// A different caller credential, model, or task cannot consume this cache.
		partition := decisionDigest([]string{config.APIKey, config.Model, options[0].Owner})
		selector.Cache = jevdom.NewCache(runtime.cacheRoot)
		selector.Cache.Namespace = partition
	}
	return selector.SelectBatch(ctx, options)
}

func (runtime *decisionRuntime) selectBatch(ctx context.Context, config jev.Config, options []jevdom.Options) ([]jevdom.Result, error) {
	key := decisionDigest(struct {
		Credential string
		Options    []jevdom.Options
	}{decisionDigest([]string{config.APIKey, config.Model}), options})
	runtime.mu.Lock()
	flight := runtime.flights[key]
	if flight == nil {
		if len(runtime.flights) >= maxDecisionFlights {
			runtime.mu.Unlock()
			return nil, errors.New("decision queue is full; retry after active decisions finish")
		}
		jobCtx, cancel := context.WithTimeout(runtime.ctx, jevdom.Timeout)
		flight = &decisionFlight{done: make(chan struct{}), cancel: cancel}
		runtime.flights[key] = flight
		runtime.started++
		go func() {
			defer cancel()
			select {
			case runtime.slots <- struct{}{}:
				flight.results, flight.err = runtime.run(jobCtx, config, options)
				<-runtime.slots
			case <-jobCtx.Done():
				flight.err = jobCtx.Err()
			}
			runtime.mu.Lock()
			flight.finished = true
			runtime.completed++
			if runtime.flights[key] == flight {
				delete(runtime.flights, key)
			}
			close(flight.done)
			runtime.mu.Unlock()
		}()
	} else {
		runtime.coalesced++
	}
	flight.waiters++
	runtime.mu.Unlock()
	defer func() {
		runtime.mu.Lock()
		flight.waiters--
		if flight.waiters == 0 && !flight.finished {
			flight.cancel()
			if runtime.flights[key] == flight {
				delete(runtime.flights, key)
			}
		}
		runtime.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-flight.done:
		return flight.results, flight.err
	}
}

func decisionResponse(id string, value any, err error) RPCResponse {
	response := RPCResponse{ID: id}
	if err != nil {
		// Relay the classified Jev cause so the CLI can report its code and next
		// steps; jev.Error messages never contain credentials or response bodies.
		code := jev.CodeOf(err)
		if code == "" {
			code = "decision_failed"
		}
		response.Error = &RPCError{Code: code, Message: err.Error()}
		return response
	}
	response.Result, err = json.Marshal(value)
	if err != nil {
		response.Error = &RPCError{Code: "decision_failed", Message: "cannot encode decision result"}
	}
	return response
}

// Every jev.* request is consumed locally, including invalid/unknown methods.
// This is the credential boundary between private CLI IPC and browser RPC.
func (runtime *decisionRuntime) handle(ctx context.Context, request RPCRequest) (RPCResponse, bool) {
	if !strings.HasPrefix(request.Method, "jev.") {
		return RPCResponse{}, false
	}
	switch request.Method {
	case "jev.capabilities":
		return decisionResponse(request.ID, jevrpc.Capabilities{Version: jevrpc.Version, PersistentSelection: true, BatchSelection: true, MaxBatch: jevrpc.MaxBatch, StepDecision: true}, nil), true
	case "jev.step":
		return runtime.handleStep(ctx, request), true
	case "jev.runtime":
		runtime.mu.Lock()
		status := map[string]any{"version": jevrpc.Version, "active_decisions": len(runtime.flights), "provider_slots": cap(runtime.slots), "active_provider_slots": len(runtime.slots), "clients": len(runtime.clients), "started": runtime.started, "coalesced": runtime.coalesced, "completed": runtime.completed}
		runtime.mu.Unlock()
		return decisionResponse(request.ID, status, nil), true
	case "jev.select", "jev.select_batch":
	default:
		return decisionResponse(request.ID, nil, errors.New("unknown host decision method")), true
	}
	if len(request.Params) > maxDecisionPayload {
		return decisionResponse(request.ID, nil, errors.New("decision request exceeds 128 KiB")), true
	}
	var input jevrpc.SelectRequest
	decoder := json.NewDecoder(bytes.NewReader(request.Params))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.Version != jevrpc.Version || len(input.Options) < 1 || len(input.Options) > jevrpc.MaxBatch || (request.Method == "jev.select" && len(input.Options) != 1) {
		return decisionResponse(request.ID, nil, errors.New("invalid versioned decision request")), true
	}
	var hostConfig *jev.Config
	if input.Credentials == nil {
		loaded, err := runtime.loadConfig()
		if err != nil {
			return decisionResponse(request.ID, nil, err), true
		}
		hostConfig = &loaded
		// JSON defaults preserve compatibility, but omitted model fields should
		// inherit the host's configured pin. Explicit caller models win.
		var original struct {
			Options []map[string]json.RawMessage `json:"options"`
		}
		_ = json.Unmarshal(request.Params, &original)
		for index := range input.Options {
			if _, explicit := original.Options[index]["model"]; !explicit {
				input.Options[index].Model = loaded.Model
			}
		}
	}
	for index, option := range input.Options {
		validated, err := option.Validate()
		if err != nil {
			return decisionResponse(request.ID, nil, err), true
		}
		if validated.Owner == "" || len(validated.Owner) > 160 || strings.ContainsAny(validated.Owner, "\x00\r\n") {
			return decisionResponse(request.ID, nil, errors.New("decision owner is required and must be bounded")), true
		}
		input.Options[index] = validated
	}
	first := input.Options[0]
	for _, option := range input.Options[1:] {
		if option.Owner != first.Owner || option.LeaseID != first.LeaseID || option.Model != first.Model || option.TabID != first.TabID {
			return decisionResponse(request.ID, nil, errors.New("batch decisions must share task, lease, model, and tab")), true
		}
	}
	config := jev.Config{Model: first.Model}
	if input.Credentials != nil {
		config.APIKey = strings.TrimSpace(input.Credentials.APIKey)
		if config.APIKey == "" || len(config.APIKey) > 8192 {
			return decisionResponse(request.ID, nil, errors.New("invalid local provider credential")), true
		}
	} else {
		config.APIKey = hostConfig.APIKey
	}
	results, err := runtime.selectBatch(ctx, config, input.Options)
	if request.Method == "jev.select" && err == nil {
		if len(results) != 1 {
			err = errors.New("invalid single decision result")
		} else {
			return decisionResponse(request.ID, results[0], nil), true
		}
	}
	return decisionResponse(request.ID, jevrpc.BatchResult{Results: results}, err), true
}

// handleStep runs one operation/target decision with the pooled provider
// client. Steps are not coalesced: identical goals can follow different
// histories. They share the provider slots with selections.
func (runtime *decisionRuntime) handleStep(ctx context.Context, request RPCRequest) RPCResponse {
	if len(request.Params) > maxDecisionPayload {
		return decisionResponse(request.ID, nil, errors.New("decision request exceeds 128 KiB"))
	}
	var input jevrpc.StepRequest
	decoder := json.NewDecoder(bytes.NewReader(request.Params))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.Version != jevrpc.Version {
		return decisionResponse(request.ID, nil, errors.New("invalid versioned step request"))
	}
	config := jev.Config{}
	if input.Credentials == nil {
		loaded, err := runtime.loadConfig()
		if err != nil {
			return decisionResponse(request.ID, nil, err)
		}
		config = loaded
		var original struct {
			Options map[string]json.RawMessage `json:"options"`
		}
		if json.Unmarshal(request.Params, &original) == nil {
			if _, explicit := original.Options["model"]; !explicit {
				input.Options.Model = loaded.Model
			}
		}
	} else {
		config.APIKey = strings.TrimSpace(input.Credentials.APIKey)
		if config.APIKey == "" || len(config.APIKey) > 8192 {
			return decisionResponse(request.ID, nil, errors.New("invalid local provider credential"))
		}
	}
	options, err := input.Options.Validate()
	if err != nil {
		return decisionResponse(request.ID, nil, err)
	}
	if options.Owner == "" || len(options.Owner) > 160 || strings.ContainsAny(options.Owner, "\x00\r\n") {
		return decisionResponse(request.ID, nil, errors.New("decision owner is required and must be bounded"))
	}
	config.Model = options.Model
	select {
	case runtime.slots <- struct{}{}:
	case <-ctx.Done():
		return decisionResponse(request.ID, nil, ctx.Err())
	}
	defer func() { <-runtime.slots }()
	runtime.mu.Lock()
	runtime.started++
	runtime.mu.Unlock()
	decision, err := runtime.step(ctx, config, options)
	runtime.mu.Lock()
	runtime.completed++
	runtime.mu.Unlock()
	return decisionResponse(request.ID, decision, err)
}

type nativeBrowserCaller struct {
	send func(any) error
}

func (caller nativeBrowserCaller) Call(ctx context.Context, method string, params any, out any) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return errors.New("cannot create browser request identity")
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return errors.New("invalid browser request")
	}
	response := forwardBrowserRPC(ctx, RPCRequest{ID: "host-" + hex.EncodeToString(nonce[:]), Method: method, Params: encoded}, caller.send)
	if response.Error != nil {
		return &bridge.RPCError{Code: response.Error.Code, Message: response.Error.Message, Data: response.Error.Data}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(response.Result, out)
}

func forwardBrowserRPC(ctx context.Context, request RPCRequest, send func(any) error) RPCResponse {
	channel := make(chan RPCResponse, 1)
	pending.Lock()
	if _, exists := pending.calls[request.ID]; exists {
		pending.Unlock()
		return RPCResponse{ID: request.ID, Error: &RPCError{Code: "duplicate_id", Message: "RPC id is already pending"}}
	}
	pending.calls[request.ID] = channel
	pending.Unlock()
	defer func() {
		pending.Lock()
		if pending.calls[request.ID] == channel {
			delete(pending.calls, request.ID)
		}
		pending.Unlock()
	}()
	command := map[string]any{"action": "rpc", "id": request.ID, "method": request.Method}
	if len(request.Params) > 0 {
		command["params"] = request.Params
	}
	if err := send(command); err != nil {
		return RPCResponse{ID: request.ID, Error: &RPCError{Code: "native_write_failed", Message: "cannot send browser request"}}
	}
	select {
	case response := <-channel:
		return response
	case <-ctx.Done():
		return RPCResponse{ID: request.ID, Error: &RPCError{Code: "rpc_timeout", Message: "browser request canceled or timed out"}}
	}
}
