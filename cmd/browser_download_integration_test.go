package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/store"
)

type fakeBrowserBridgeReply struct {
	result interface{}
	rpcErr *bridge.RPCError
	delay  time.Duration
}

type fakeBrowserBridgeServer struct {
	listener net.Listener
	handler  func(bridge.RPCRequest) fakeBrowserBridgeReply

	mu       sync.Mutex
	requests []bridge.RPCRequest
	closed   chan struct{}
	workers  sync.WaitGroup
	close    sync.Once
}

func newFakeBrowserBridgeServer(t *testing.T, handler func(bridge.RPCRequest) fakeBrowserBridgeReply) *fakeBrowserBridgeServer {
	t.Helper()
	// Darwin's AF_UNIX path limit is short enough that t.TempDir plus a long
	// test name can exceed it. Keep this transport fixture directly under /tmp.
	dir, err := os.MkdirTemp("/tmp", "rep-bdl-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "bridge.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	registry := bridge.Registry{
		PID:       os.Getpid(),
		Browser:   "arc",
		Socket:    socket,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		LastSeen:  time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.Marshal(registry)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bridge-test.json"), data, 0600); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	t.Setenv("REP_BRIDGE_DIR", dir)

	server := &fakeBrowserBridgeServer{
		listener: listener,
		handler:  handler,
		closed:   make(chan struct{}),
	}
	go server.serve()
	t.Cleanup(func() {
		server.Close()
	})
	return server
}

func (server *fakeBrowserBridgeServer) serve() {
	defer close(server.closed)
	for {
		conn, err := server.listener.Accept()
		if err != nil {
			return
		}
		server.workers.Add(1)
		go func() {
			defer server.workers.Done()
			defer conn.Close()
			var request bridge.RPCRequest
			if json.NewDecoder(conn).Decode(&request) != nil {
				return
			}
			server.mu.Lock()
			server.requests = append(server.requests, request)
			reply := server.handler(request)
			server.mu.Unlock()
			if reply.delay > 0 {
				time.Sleep(reply.delay)
			}
			var raw json.RawMessage
			if reply.result != nil {
				raw, _ = json.Marshal(reply.result)
			}
			_ = json.NewEncoder(conn).Encode(bridge.RPCResponse{
				ID: request.ID, Result: raw, Error: reply.rpcErr,
			})
		}()
	}
}

func (server *fakeBrowserBridgeServer) Close() {
	server.close.Do(func() {
		_ = server.listener.Close()
		<-server.closed
		server.workers.Wait()
	})
}

func (server *fakeBrowserBridgeServer) Requests() []bridge.RPCRequest {
	server.mu.Lock()
	defer server.mu.Unlock()
	return append([]bridge.RPCRequest(nil), server.requests...)
}

func fakeBrowserDownloadHandler(readReplies ...fakeBrowserBridgeReply) func(bridge.RPCRequest) fakeBrowserBridgeReply {
	readIndex := 0
	return func(request bridge.RPCRequest) fakeBrowserBridgeReply {
		switch fakeBrowserRequestName(request) {
		case "bridge.ping":
			return fakeBrowserBridgeReply{result: map[string]interface{}{"ok": true}}
		case "browser.create":
			return fakeBrowserBridgeReply{result: map[string]interface{}{"created": true, "tab_id": 77}}
		case "browser.attach":
			return fakeBrowserBridgeReply{result: map[string]interface{}{"attached": true}}
		case "browser.cdp/Page.getFrameTree":
			return fakeBrowserBridgeReply{result: map[string]interface{}{
				"result": map[string]interface{}{
					"frameTree": map[string]interface{}{"frame": map[string]interface{}{"id": "main-frame"}},
				},
			}}
		case "browser.cdp/Network.loadNetworkResource":
			return fakeBrowserBridgeReply{result: map[string]interface{}{
				"result": map[string]interface{}{
					"resource": map[string]interface{}{
						"success": true, "httpStatusCode": 200, "stream": "stream-1",
						"headers": map[string]interface{}{"Content-Type": "application/zip; charset=binary"},
					},
				},
			}}
		case "browser.cdp/IO.read":
			if readIndex >= len(readReplies) {
				return fakeBrowserBridgeReply{rpcErr: &bridge.RPCError{Code: "unexpected_read", Message: "unexpected extra IO.read"}}
			}
			reply := readReplies[readIndex]
			readIndex++
			return reply
		case "browser.cdp/IO.close":
			return fakeBrowserBridgeReply{result: map[string]interface{}{"result": map[string]interface{}{}}}
		case "browser.detach":
			return fakeBrowserBridgeReply{result: map[string]interface{}{"detached": true}}
		case "browser.close":
			return fakeBrowserBridgeReply{result: map[string]interface{}{"closed": true}}
		default:
			return fakeBrowserBridgeReply{rpcErr: &bridge.RPCError{Code: "unknown_method", Message: fakeBrowserRequestName(request)}}
		}
	}
}

func fakeBrowserRequestName(request bridge.RPCRequest) string {
	if request.Method != "browser.cdp" {
		return request.Method
	}
	params, _ := request.Params.(map[string]interface{})
	method, _ := params["method"].(string)
	return request.Method + "/" + method
}

func fakeBrowserRequestNames(requests []bridge.RPCRequest) []string {
	names := make([]string, 0, len(requests))
	for _, request := range requests {
		names = append(names, fakeBrowserRequestName(request))
	}
	return names
}

func decodeFakeBrowserParams(t *testing.T, request bridge.RPCRequest, out interface{}) {
	t.Helper()
	data, err := json.Marshal(request.Params)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatal(err)
	}
}

func assertNoBrowserDownloadPartFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".part") {
			t.Fatalf("temporary part file survived: %s", entry.Name())
		}
	}
}

func TestBrowserDownloadBridgeProtocolStreamsPlainAndBase64ThenPublishesAtomically(t *testing.T) {
	payload := []byte("PK\x03\x04browser-download-integration")
	dir := t.TempDir()
	destination := filepath.Join(dir, "artifact.ipa")
	protocolHandler := fakeBrowserDownloadHandler(
		fakeBrowserBridgeReply{result: map[string]interface{}{
			"result": map[string]interface{}{"data": "PK", "base64Encoded": false, "eof": false},
		}},
		fakeBrowserBridgeReply{result: map[string]interface{}{
			"result": map[string]interface{}{
				"data": base64.StdEncoding.EncodeToString(payload[2:]), "base64Encoded": true, "eof": true,
			},
		}},
	)
	var observationMu sync.Mutex
	publishedBeforeProtocolFinished := false
	server := newFakeBrowserBridgeServer(t, func(request bridge.RPCRequest) fakeBrowserBridgeReply {
		if _, err := os.Lstat(destination); err == nil {
			observationMu.Lock()
			publishedBeforeProtocolFinished = true
			observationMu.Unlock()
		}
		return protocolHandler(request)
	})
	request := &store.Request{ID: "h_download", Method: "GET", URL: "https://download.example.test/artifact.ipa?signature=secret"}

	result, err := downloadCapturedRequestInBrowser(context.Background(), request, destination, browserDownloadOptions{
		Timeout: 3 * time.Second, ChunkSize: minimumBrowserDownloadChunk,
		NoCache: true, OmitCredentials: true,
		Validation: artifactValidationOptions{
			MinimumBytes: int64(len(payload)), ExpectedContentTypes: []string{"application/zip"}, ExpectedMagic: "ipa",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, payload) {
		t.Fatalf("published payload = %q, want %q", got, payload)
	}
	wantDigest := sha256.Sum256(payload)
	if result.RequestID != request.ID || result.Status != 200 || result.Bytes != int64(len(payload)) || result.SHA256 != hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.ContentType != "application/zip; charset=binary" || result.DetectedContentType != "application/zip" || result.TabID != 77 || !result.TabClosed {
		t.Fatalf("unexpected transfer metadata: %+v", result)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("published mode = %o", info.Mode().Perm())
	}
	observationMu.Lock()
	publishedEarly := publishedBeforeProtocolFinished
	observationMu.Unlock()
	if publishedEarly {
		t.Fatal("destination became visible before the browser protocol and cleanup finished")
	}
	assertNoBrowserDownloadPartFiles(t, dir)

	requests := server.Requests()
	wantProtocol := []string{
		"bridge.ping",
		"browser.create",
		"browser.attach",
		"browser.cdp/Page.getFrameTree",
		"browser.cdp/Network.loadNetworkResource",
		"browser.cdp/IO.read",
		"browser.cdp/IO.read",
		"browser.cdp/IO.close",
		"browser.detach",
		"browser.close",
	}
	if gotProtocol := fakeBrowserRequestNames(requests); !reflect.DeepEqual(gotProtocol, wantProtocol) {
		t.Fatalf("bridge protocol = %v, want %v", gotProtocol, wantProtocol)
	}

	var create struct {
		URL    string `json:"url"`
		Active bool   `json:"active"`
	}
	decodeFakeBrowserParams(t, requests[1], &create)
	if create.URL != "about:blank" || create.Active {
		t.Fatalf("browser.create params = %+v", create)
	}
	var load struct {
		TabID  int    `json:"tab_id"`
		Method string `json:"method"`
		Params struct {
			FrameID string `json:"frameId"`
			URL     string `json:"url"`
			Options struct {
				DisableCache       bool `json:"disableCache"`
				IncludeCredentials bool `json:"includeCredentials"`
			} `json:"options"`
		} `json:"command_params"`
	}
	decodeFakeBrowserParams(t, requests[4], &load)
	if load.TabID != 77 || load.Method != "Network.loadNetworkResource" || load.Params.FrameID != "main-frame" || load.Params.URL != request.URL || !load.Params.Options.DisableCache || load.Params.Options.IncludeCredentials {
		t.Fatalf("Network.loadNetworkResource params = %+v", load)
	}
	var read struct {
		TabID  int    `json:"tab_id"`
		Method string `json:"method"`
		Params struct {
			Handle string `json:"handle"`
			Size   int    `json:"size"`
		} `json:"command_params"`
	}
	decodeFakeBrowserParams(t, requests[5], &read)
	if read.TabID != 77 || read.Method != "IO.read" || read.Params.Handle != "stream-1" || read.Params.Size != minimumBrowserDownloadChunk {
		t.Fatalf("IO.read params = %+v", read)
	}
}

func TestBrowserDownloadValidatorFailureStillClosesStreamDetachAndTab(t *testing.T) {
	body := []byte("<!doctype html><html><body>quota exceeded</body></html>")
	server := newFakeBrowserBridgeServer(t, fakeBrowserDownloadHandler(
		fakeBrowserBridgeReply{result: map[string]interface{}{
			"result": map[string]interface{}{"data": string(body), "base64Encoded": false, "eof": true},
		}},
	))
	dir := t.TempDir()
	destination := filepath.Join(dir, "artifact.ipa")

	_, err := downloadCapturedRequestInBrowser(context.Background(), &store.Request{
		ID: "h_quota", Method: "GET", URL: "https://download.example.test/artifact.ipa?signature=secret",
	}, destination, browserDownloadOptions{Timeout: 3 * time.Second, ChunkSize: minimumBrowserDownloadChunk})
	if err == nil || !strings.Contains(err.Error(), "HTML response") {
		t.Fatalf("expected validator failure, got %v", err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid artifact was published: %v", statErr)
	}
	assertNoBrowserDownloadPartFiles(t, dir)
	assertBrowserDownloadCleanupProtocol(t, server.Requests(), 1)
}

func TestBrowserDownloadIOErrorStillClosesStreamDetachAndTab(t *testing.T) {
	server := newFakeBrowserBridgeServer(t, fakeBrowserDownloadHandler(
		fakeBrowserBridgeReply{rpcErr: &bridge.RPCError{Code: "io_failure", Message: "synthetic read failure"}},
	))
	dir := t.TempDir()
	destination := filepath.Join(dir, "artifact.ipa")

	_, err := downloadCapturedRequestInBrowser(context.Background(), &store.Request{
		ID: "h_io_error", Method: "GET", URL: "https://download.example.test/artifact.ipa?signature=secret",
	}, destination, browserDownloadOptions{Timeout: 3 * time.Second, ChunkSize: minimumBrowserDownloadChunk})
	if err == nil || !strings.Contains(err.Error(), "IO.read failed") || !strings.Contains(err.Error(), "io_failure") {
		t.Fatalf("expected IO.read failure, got %v", err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed artifact was published: %v", statErr)
	}
	assertNoBrowserDownloadPartFiles(t, dir)
	assertBrowserDownloadCleanupProtocol(t, server.Requests(), 1)
}

func TestBrowserDownloadTimeoutStillClosesStreamDetachAndTab(t *testing.T) {
	server := newFakeBrowserBridgeServer(t, fakeBrowserDownloadHandler(
		fakeBrowserBridgeReply{
			result: map[string]interface{}{
				"result": map[string]interface{}{"data": "PK", "base64Encoded": false, "eof": true},
			},
			delay: 750 * time.Millisecond,
		},
	))
	dir := t.TempDir()
	destination := filepath.Join(dir, "artifact.ipa")

	_, err := downloadCapturedRequestInBrowser(context.Background(), &store.Request{
		ID: "h_timeout", Method: "GET", URL: "https://download.example.test/artifact.ipa?signature=secret",
	}, destination, browserDownloadOptions{Timeout: 250 * time.Millisecond, ChunkSize: minimumBrowserDownloadChunk})
	if err == nil || !strings.Contains(err.Error(), "IO.read failed") {
		t.Fatalf("expected timed-out IO.read, got %v", err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("timed-out artifact was published: %v", statErr)
	}
	assertNoBrowserDownloadPartFiles(t, dir)
	assertBrowserDownloadCleanupProtocol(t, server.Requests(), 1)
}

func assertBrowserDownloadCleanupProtocol(t *testing.T, requests []bridge.RPCRequest, readCount int) {
	t.Helper()
	want := []string{
		"bridge.ping",
		"browser.create",
		"browser.attach",
		"browser.cdp/Page.getFrameTree",
		"browser.cdp/Network.loadNetworkResource",
	}
	for i := 0; i < readCount; i++ {
		want = append(want, "browser.cdp/IO.read")
	}
	want = append(want, "browser.cdp/IO.close", "browser.detach", "browser.close")
	if got := fakeBrowserRequestNames(requests); !reflect.DeepEqual(got, want) {
		t.Fatalf("bridge protocol = %v, want %v", got, want)
	}

	closeRequest := requests[len(requests)-3]
	var closeParams struct {
		TabID  int    `json:"tab_id"`
		Method string `json:"method"`
		Params struct {
			Handle string `json:"handle"`
		} `json:"command_params"`
	}
	decodeFakeBrowserParams(t, closeRequest, &closeParams)
	if closeParams.TabID != 77 || closeParams.Method != "IO.close" || closeParams.Params.Handle != "stream-1" {
		t.Fatalf("IO.close params = %+v", closeParams)
	}
	var detach struct {
		TabID int `json:"tab_id"`
	}
	decodeFakeBrowserParams(t, requests[len(requests)-2], &detach)
	if detach.TabID != 77 {
		t.Fatalf("browser.detach params = %+v", detach)
	}
	var closeTab struct {
		TabID int `json:"tab_id"`
	}
	decodeFakeBrowserParams(t, requests[len(requests)-1], &closeTab)
	if closeTab.TabID != 77 {
		t.Fatalf("browser.close params = %+v", closeTab)
	}
}
