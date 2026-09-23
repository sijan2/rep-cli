// rep-host is both the rep+ native-messaging host and the local RPC relay used
// by `rep browser`. Chrome/Arc owns this process; the CLI reaches it through a
// per-process Unix socket registered under the rep data directory.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/repplus/rep-cli/internal/store"
)

const (
	LiveFileName           = "live.json"
	MaxLiveRequests        = 10000
	maxNativeMessageBytes  = 32 * 1024 * 1024
	maxNativeResponseBytes = 1024 * 1024
	maxSocketMessageBytes  = 8 * 1024 * 1024
	flushDelay             = 200 * time.Millisecond
)

type RPCError struct {
	Code    string      `json:"code,omitempty"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type Message struct {
	Type             string          `json:"type,omitempty"`
	Action           string          `json:"action,omitempty"`
	ID               string          `json:"id,omitempty"`
	Method           string          `json:"method,omitempty"`
	Params           json.RawMessage `json:"params,omitempty"`
	Result           json.RawMessage `json:"result,omitempty"`
	Error            *RPCError       `json:"error,omitempty"`
	Requests         []Request       `json:"requests,omitempty"`
	Request          *Request        `json:"request,omitempty"`
	SessionID        string          `json:"session_id,omitempty"`
	URL              string          `json:"url,omitempty"`
	TabID            int             `json:"tab_id,omitempty"`
	Browser          string          `json:"browser,omitempty"`
	BrowserLabel     string          `json:"browser_label,omitempty"`
	ExtensionID      string          `json:"extension_id,omitempty"`
	ExtensionVersion string          `json:"extension_version,omitempty"`
	UserAgent        string          `json:"user_agent,omitempty"`
	CaptureMode      string          `json:"capture_mode,omitempty"`
	TimedOut         bool            `json:"timed_out,omitempty"`
	ExpectedRequests *int            `json:"expected_requests,omitempty"`
	CaptureWarnings  []string        `json:"capture_warnings,omitempty"`
	TransferID       string          `json:"transfer_id,omitempty"`
	Sequence         int             `json:"sequence,omitempty"`
	Chunks           int             `json:"chunks,omitempty"`
	TotalBytes       int64           `json:"total_bytes,omitempty"`
	SHA256           string          `json:"sha256,omitempty"`
	Data             string          `json:"data,omitempty"`
}

type Request = store.Request
type Response = store.Response
type BrowserSession = store.BrowserSession

type LiveData struct {
	Version    string          `json:"version"`
	ExportedAt string          `json:"exported_at"`
	SessionID  string          `json:"session_id,omitempty"`
	Browser    *BrowserSession `json:"browser_session,omitempty"`
	Requests   []Request       `json:"requests"`
}

type RPCRequest struct {
	ID        string          `json:"id"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params,omitempty"`
	TimeoutMS int64           `json:"timeout_ms,omitempty"`
}

type RPCResponse struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

type Registry struct {
	PID          int    `json:"pid"`
	ParentPID    int    `json:"parent_pid,omitempty"`
	Browser      string `json:"browser"`
	BrowserLabel string `json:"browser_label,omitempty"`
	Socket       string `json:"socket"`
	ExtensionID  string `json:"extension_id,omitempty"`
	ExtensionVer string `json:"extension_version,omitempty"`
	UserAgent    string `json:"user_agent,omitempty"`
	StartedAt    string `json:"started_at"`
	LastSeen     string `json:"last_seen"`
}

var (
	liveMu               sync.Mutex
	liveData             *LiveData
	dataPath             string
	hostBrowser          string
	browserCaptureActive bool
	browserCaptureSealed bool

	stdoutMu sync.Mutex
	pending  = struct {
		sync.Mutex
		calls map[string]chan RPCResponse
	}{calls: make(map[string]chan RPCResponse)}

	dirtyCh = make(chan struct{}, 1)
)

func main() {
	var keepFlag bool
	flag.BoolVar(&keepFlag, "keep", true, "Keep live.json data when the browser disconnects")
	flag.Parse()
	_ = keepFlag

	dataPath = getDataPath()
	if err := os.MkdirAll(filepath.Dir(dataPath), 0700); err != nil {
		logError("create data directory", err)
		return
	}
	if err := initializeCaptureSnapshots(); err != nil {
		logError("initialize capture snapshots", err)
		return
	}
	defer closeCaptureSnapshots()
	liveData = loadLiveData()
	if liveData.Browser != nil && liveData.Browser.FinishedAt != "" {
		browserCaptureSealed = true
	}
	if liveData.SessionID == "" {
		liveData.SessionID = generateSessionID()
	}

	server, err := startBridgeServer()
	if err != nil {
		logError("start bridge server", err)
		return
	}
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go flushLoop(ctx)

	for {
		message, err := readMessage(os.Stdin)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				logError("read native message", err)
			}
			break
		}
		server.Touch(message)
		if message.Action == "rpc_result" {
			routeRPCResponse(message)
			continue
		}
		if dispatchJev(ctx, message, writeMessage) {
			continue
		}
		response, flushNow := handleMessage(message)
		if message.Action == "session_end" && response["success"] == true {
			if err := sealCaptureSnapshot(); err != nil {
				logError("seal capture snapshot", err)
				rememberCaptureFailure(message.SessionID, err.Error())
				response["success"], response["error"] = false, err.Error()
			}
		}
		if flushNow {
			if err := saveLiveData(); err != nil {
				logError("flush live data", err)
				response["success"], response["error"] = false, "write live capture failed"
			}
		}
		if response != nil {
			if err := writeMessage(response); err != nil {
				logError("write native response", err)
				break
			}
		}
	}
	cancel()
	liveMu.Lock()
	cleanupRequestTransfersLocked()
	liveMu.Unlock()
	if err := saveLiveData(); err != nil {
		logError("final live-data flush", err)
	}
}

func generateSessionID() string {
	return time.Now().Format("20060102-150405.000")
}

func getDataPath() string {
	if override := os.Getenv("REPLIVE_PATH"); override != "" {
		if path, err := expandHomePath(override); err == nil {
			return path
		}
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "rep-cli", LiveFileName)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "rep-cli", LiveFileName)
}

func getBridgeDir() (string, error) {
	if override := os.Getenv("REP_BRIDGE_DIR"); override != "" {
		return expandHomePath(override)
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "rep-cli", "bridges"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "rep-cli", "bridges"), nil
}

func expandHomePath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

func loadLiveData() *LiveData {
	initial := &LiveData{Version: "2.0", Requests: []Request{}}
	content, err := os.ReadFile(dataPath)
	if err != nil {
		return initial
	}
	if err := json.Unmarshal(content, initial); err != nil {
		logError("parse existing live.json; starting fresh", err)
		return &LiveData{Version: "2.0", Requests: []Request{}}
	}
	if initial.Requests == nil {
		initial.Requests = []Request{}
	}
	initial.Version = "2.0"
	return initial
}

func saveLiveData() error {
	liveMu.Lock()
	defer liveMu.Unlock()
	liveData.ExportedAt = time.Now().UTC().Format(time.RFC3339Nano)
	_, _, err := writeLiveDataFile(dataPath, liveData, 0)
	return err
}

func atomicWriteFile(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".rep-live-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func signalDirty() {
	select {
	case dirtyCh <- struct{}{}:
	default:
	}
}

func flushLoop(ctx context.Context) {
	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-dirtyCh:
			if timer == nil {
				timer = time.NewTimer(flushDelay)
				timerC = timer.C
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(flushDelay)
			}
		case <-timerC:
			if err := saveLiveData(); err != nil {
				logError("flush live data", err)
			}
			timer = nil
			timerC = nil
		}
	}
}

func handleMessage(message *Message) (map[string]interface{}, bool) {
	if message == nil {
		return map[string]interface{}{"success": false, "error": "empty message"}, false
	}
	if message.Action == "hello" {
		liveMu.Lock()
		count := len(liveData.Requests)
		liveMu.Unlock()
		return map[string]interface{}{"success": true, "action": "hello", "path": dataPath, "count": count}, false
	}

	liveMu.Lock()
	flushNow := false
	changed := false
	response := map[string]interface{}{"success": true, "action": message.Action}
	if browserCaptureActive {
		switch message.Action {
		case "sync", "clear", "ambient_begin", "ambient_resume", "ambient_end", "session_begin":
			liveMu.Unlock()
			return map[string]interface{}{"success": false, "action": message.Action, "error": "capture is active; finish it before resetting capture state"}, false
		}
	}
	if message.Action == "session_end" && message.SessionID == "" {
		liveMu.Unlock()
		return map[string]interface{}{"success": false, "action": message.Action, "error": "capture session id is required"}, false
	}
	if (message.Action == "add" || message.Action == "add_many") && message.SessionID == "" && (browserCaptureActive || browserCaptureSealed) {
		liveMu.Unlock()
		return map[string]interface{}{"success": true, "action": message.Action, "ignored": true}, false
	}
	// Session-tagged updates can never attach to a different or sealed capture.
	if (message.Action == "request_chunk" || message.Action == "request_end") && message.SessionID == "" {
		liveMu.Unlock()
		return map[string]interface{}{"success": false, "action": message.Action, "error": "capture session id is required for request transfers"}, false
	}
	if (message.Action == "add" || message.Action == "add_many" || message.Action == "session_end" || message.Action == "request_chunk" || message.Action == "request_end") && message.SessionID != "" && (message.SessionID != liveData.SessionID || !browserCaptureActive) {
		liveMu.Unlock()
		return map[string]interface{}{"success": false, "action": message.Action, "error": "capture session mismatch or already sealed"}, false
	}
	switch message.Action {
	case "request_chunk", "request_end":
		var err error
		if message.Action == "request_chunk" {
			err = acceptRequestChunkLocked(message)
		} else {
			err = finishRequestTransferLocked(message)
			changed = err == nil
		}
		if err != nil {
			failCaptureLocked(err.Error())
			cleanupRequestTransfersLocked()
			response["success"], response["error"] = false, err.Error()
		}
	case "add":
		if message.Request == nil {
			response = map[string]interface{}{"success": false, "error": "missing request", "action": "add"}
			break
		}
		if shouldIgnoreRequestLocked(*message.Request) {
			response["ignored"] = true
		} else {
			upsertRequestLocked(*message.Request)
			changed = true
		}
	case "add_many":
		for i := range message.Requests {
			if !shouldIgnoreRequestLocked(message.Requests[i]) {
				upsertRequestLocked(message.Requests[i])
				changed = true
			}
		}
	case "sync":
		liveData.Requests = append([]Request(nil), message.Requests...)
		trimRequestsLocked()
		browserCaptureActive = false
		browserCaptureSealed = false
		changed = true
		flushNow = true
	case "clear":
		liveData.Requests = []Request{}
		liveData.SessionID = generateSessionID()
		liveData.Browser = nil
		browserCaptureActive = false
		browserCaptureSealed = false
		changed = true
		flushNow = true
	case "session_begin":
		cleanupRequestTransfersLocked()
		liveData.Requests = []Request{}
		if message.SessionID == "" {
			message.SessionID = generateSessionID()
		}
		liveData.SessionID = message.SessionID
		browserName := message.Browser
		if browserName == "" {
			browserName = hostBrowser
		}
		liveData.Browser = &BrowserSession{
			Browser: browserName, URL: message.URL, TabID: message.TabID,
			CaptureMode: message.CaptureMode, StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		browserCaptureActive = true
		browserCaptureSealed = false
		changed = true
		flushNow = true
	case "session_end":
		if liveData.Browser == nil {
			liveData.Browser = &BrowserSession{}
		}
		if message.URL != "" {
			liveData.Browser.URL = message.URL
		}
		liveData.Browser.TimedOut = message.TimedOut
		liveData.Browser.ExpectedRequests = message.ExpectedRequests
		liveData.Browser.CaptureWarnings = append([]string(nil), message.CaptureWarnings...)
		liveData.Browser.ReceivedRequests = len(liveData.Requests)
		if len(requestTransfers) > 0 {
			failCaptureLocked("capture ended with incomplete request transfers")
		}
		if message.ExpectedRequests != nil && (*message.ExpectedRequests < 0 || *message.ExpectedRequests != len(liveData.Requests)) {
			failCaptureLocked(fmt.Sprintf("capture request count mismatch: expected %d, received %d", *message.ExpectedRequests, len(liveData.Requests)))
		}
		cleanupRequestTransfersLocked()
		if liveData.Browser.CaptureError != "" {
			response["success"], response["error"] = false, liveData.Browser.CaptureError
		}
		liveData.Browser.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		browserCaptureActive = false
		browserCaptureSealed = true
		changed = true
		flushNow = true
	case "ambient_begin":
		liveData.Requests = []Request{}
		if message.SessionID == "" {
			message.SessionID = "ambient-" + generateSessionID()
		}
		liveData.SessionID = message.SessionID
		liveData.Browser = &BrowserSession{
			Browser: hostBrowser, CaptureMode: "ambient",
			StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}
		browserCaptureActive = false
		browserCaptureSealed = false
		changed = true
		flushNow = true
	case "ambient_resume":
		if liveData.Browser == nil || liveData.Browser.CaptureMode != "ambient" || liveData.Browser.FinishedAt != "" {
			liveData.Requests = []Request{}
			if message.SessionID == "" {
				message.SessionID = "ambient-" + generateSessionID()
			}
			liveData.SessionID = message.SessionID
			liveData.Browser = &BrowserSession{
				Browser: hostBrowser, CaptureMode: "ambient",
				StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
			}
			changed = true
			flushNow = true
		}
		browserCaptureActive = false
		browserCaptureSealed = false
		response["resumed"] = true
	case "ambient_end":
		if liveData.Browser == nil {
			liveData.Browser = &BrowserSession{Browser: hostBrowser, CaptureMode: "ambient"}
		}
		liveData.Browser.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		browserCaptureActive = false
		browserCaptureSealed = true
		changed = true
		flushNow = true
	case "ping":
		response["action"] = "pong"
		response["path"] = dataPath
	default:
		response = map[string]interface{}{"success": false, "error": "unknown action", "action": message.Action}
	}
	response["count"] = len(liveData.Requests)
	liveMu.Unlock()
	if changed && !flushNow {
		signalDirty()
	}
	return response, flushNow
}

func shouldIgnoreRequestLocked(request Request) bool {
	return request.CaptureSource == "webrequest" && (browserCaptureActive || browserCaptureSealed)
}

func upsertRequestLocked(incoming Request) {
	if incoming.ID != "" {
		for i := len(liveData.Requests) - 1; i >= 0; i-- {
			if liveData.Requests[i].ID == incoming.ID {
				liveData.Requests[i] = mergeRequest(liveData.Requests[i], incoming)
				return
			}
		}
	}
	if incoming.CaptureSource == "devtools" || incoming.CaptureSource == "cdp" {
		for i := len(liveData.Requests) - 1; i >= 0 && i >= len(liveData.Requests)-80; i-- {
			current := liveData.Requests[i]
			if current.CaptureSource != "webrequest" || current.Method != incoming.Method || current.URL != incoming.URL {
				continue
			}
			delta := current.Timestamp - incoming.Timestamp
			if delta < 0 {
				delta = -delta
			}
			if delta <= 2500 {
				liveData.Requests[i] = mergeRequest(current, incoming)
				return
			}
		}
	}
	liveData.Requests = append(liveData.Requests, incoming)
	trimRequestsLocked()
}

func mergeRequest(old, incoming Request) Request {
	incomingBodyExplicit := incoming.ResponseBodyCapture != nil
	if incoming.ID == "" {
		incoming.ID = old.ID
	}
	if incoming.OriginalID == "" {
		incoming.OriginalID = old.OriginalID
	}
	if incoming.Headers == nil {
		incoming.Headers = old.Headers
	}
	if incoming.Body == "" && (incoming.RequestBodyCapture == nil || incoming.RequestBodyCapture.State != "complete") {
		incoming.Body = old.Body
	}
	if incoming.RequestBodyCapture == nil {
		incoming.RequestBodyCapture = old.RequestBodyCapture
	}
	if incoming.ResponseBodyCapture == nil {
		incoming.ResponseBodyCapture = old.ResponseBodyCapture
		if incoming.ResponseEncoding == "" {
			incoming.ResponseEncoding = old.ResponseEncoding
		}
		incoming.ResponseBodyTruncated = incoming.ResponseBodyTruncated || old.ResponseBodyTruncated
		if incoming.ResponseBodyError == "" {
			incoming.ResponseBodyError = old.ResponseBodyError
		}
	}
	if incoming.NetworkState == "" {
		incoming.NetworkState = old.NetworkState
	}
	if incoming.Response == nil {
		incoming.Response = old.Response
	} else if old.Response != nil {
		if incoming.Response.Headers == nil {
			incoming.Response.Headers = old.Response.Headers
		}
		if incoming.Response.Body == "" && (!incomingBodyExplicit || incoming.ResponseBodyCapture.State != "complete") {
			incoming.Response.Body = old.Response.Body
		}
	}
	if incoming.Timestamp == 0 {
		incoming.Timestamp = old.Timestamp
	}
	if incoming.PageURL == "" {
		incoming.PageURL = old.PageURL
	}
	if incoming.Initiator == "" {
		incoming.Initiator = old.Initiator
	}
	if incoming.ResourceType == "" {
		incoming.ResourceType = old.ResourceType
	}
	return incoming
}

func trimRequestsLocked() {
	if len(liveData.Requests) > activeCaptureLimits.requests {
		dropped := len(liveData.Requests) - activeCaptureLimits.requests
		if browserCaptureActive {
			failCaptureLocked(fmt.Sprintf("capture exceeds REP_CAPTURE_MAX_REQUESTS (%d); capture is incomplete", activeCaptureLimits.requests))
			liveData.Browser.DroppedRequests += dropped
			liveData.Requests = liveData.Requests[:activeCaptureLimits.requests]
		} else {
			liveData.Requests = append([]Request(nil), liveData.Requests[dropped:]...)
			if liveData.Browser == nil {
				liveData.Browser = &BrowserSession{}
			}
			liveData.Browser.DroppedRequests += dropped
		}
	}
}

func readMessage(reader io.Reader) (*Message, error) {
	var length uint32
	if err := binary.Read(reader, binary.LittleEndian, &length); err != nil {
		return nil, err
	}
	if length == 0 || length > maxNativeMessageBytes {
		return nil, fmt.Errorf("invalid native message length %d", length)
	}
	content := make([]byte, length)
	if _, err := io.ReadFull(reader, content); err != nil {
		return nil, err
	}
	var message Message
	if err := json.Unmarshal(content, &message); err != nil {
		return nil, err
	}
	return &message, nil
}

func writeMessage(message interface{}) error {
	content, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(content) > maxNativeResponseBytes {
		return fmt.Errorf("native response is too large: %d bytes", len(content))
	}
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	if err := binary.Write(os.Stdout, binary.LittleEndian, uint32(len(content))); err != nil {
		return err
	}
	_, err = os.Stdout.Write(content)
	return err
}

func routeRPCResponse(message *Message) {
	response := RPCResponse{ID: message.ID, Result: message.Result, Error: message.Error}
	pending.Lock()
	channel := pending.calls[message.ID]
	if channel != nil {
		delete(pending.calls, message.ID)
	}
	pending.Unlock()
	if channel != nil {
		select {
		case channel <- response:
		default:
		}
	}
}

type bridgeServer struct {
	listener     net.Listener
	socketPath   string
	registryPath string
	registryMu   sync.Mutex
	registry     Registry
	closeOnce    sync.Once
}

func startBridgeServer() (*bridgeServer, error) {
	dir, err := getBridgeDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	pid := os.Getpid()
	socketPath := filepath.Join(dir, fmt.Sprintf("bridge-%d.sock", pid))
	registryPath := filepath.Join(dir, fmt.Sprintf("bridge-%d.json", pid))
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socketPath, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	browser, label := detectBrowserFromParents()
	hostBrowser = browser
	now := time.Now().UTC().Format(time.RFC3339Nano)
	server := &bridgeServer{
		listener: listener, socketPath: socketPath, registryPath: registryPath,
		registry: Registry{
			PID: pid, ParentPID: os.Getppid(), Browser: browser, BrowserLabel: label,
			Socket: socketPath, StartedAt: now, LastSeen: now,
		},
	}
	if err := server.writeRegistry(); err != nil {
		listener.Close()
		return nil, err
	}
	go server.serve()
	return server, nil
}

func (server *bridgeServer) serve() {
	for {
		conn, err := server.listener.Accept()
		if err != nil {
			return
		}
		go server.handleConnection(conn)
	}
}

func (server *bridgeServer) handleConnection(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	decoder := json.NewDecoder(io.LimitReader(conn, maxSocketMessageBytes))
	var request RPCRequest
	if err := decoder.Decode(&request); err != nil {
		_ = json.NewEncoder(conn).Encode(RPCResponse{Error: &RPCError{Code: "invalid_request", Message: err.Error()}})
		return
	}
	if request.ID == "" || request.Method == "" {
		_ = json.NewEncoder(conn).Encode(RPCResponse{ID: request.ID, Error: &RPCError{Code: "invalid_request", Message: "id and method are required"}})
		return
	}
	if response, handled := handleCaptureRPC(request); handled {
		_ = json.NewEncoder(conn).Encode(response)
		return
	}
	timeout := 35 * time.Second
	if request.TimeoutMS > 0 {
		timeout = time.Duration(request.TimeoutMS) * time.Millisecond
		if timeout > 2*time.Minute {
			timeout = 2 * time.Minute
		}
	}
	channel := make(chan RPCResponse, 1)
	pending.Lock()
	if _, exists := pending.calls[request.ID]; exists {
		pending.Unlock()
		_ = json.NewEncoder(conn).Encode(RPCResponse{ID: request.ID, Error: &RPCError{Code: "duplicate_id", Message: "RPC id is already pending"}})
		return
	}
	pending.calls[request.ID] = channel
	pending.Unlock()

	command := map[string]interface{}{"action": "rpc", "id": request.ID, "method": request.Method}
	if len(request.Params) > 0 {
		command["params"] = json.RawMessage(request.Params)
	}
	if err := writeMessage(command); err != nil {
		pending.Lock()
		delete(pending.calls, request.ID)
		pending.Unlock()
		_ = json.NewEncoder(conn).Encode(RPCResponse{ID: request.ID, Error: &RPCError{Code: "native_write_failed", Message: err.Error()}})
		return
	}

	var response RPCResponse
	select {
	case response = <-channel:
	case <-time.After(timeout):
		pending.Lock()
		delete(pending.calls, request.ID)
		pending.Unlock()
		response = RPCResponse{ID: request.ID, Error: &RPCError{Code: "rpc_timeout", Message: fmt.Sprintf("%s timed out after %s", request.Method, timeout)}}
	}
	_ = json.NewEncoder(conn).Encode(response)
}

func (server *bridgeServer) Touch(message *Message) {
	server.registryMu.Lock()
	server.registry.LastSeen = time.Now().UTC().Format(time.RFC3339Nano)
	if message != nil && message.Action == "hello" {
		if message.Browser != "" {
			server.registry.Browser = strings.ToLower(message.Browser)
		}
		if message.BrowserLabel != "" {
			server.registry.BrowserLabel = message.BrowserLabel
		}
		server.registry.ExtensionID = message.ExtensionID
		server.registry.ExtensionVer = message.ExtensionVersion
		server.registry.UserAgent = message.UserAgent
	}
	server.registryMu.Unlock()
	_ = server.writeRegistry()
}

func (server *bridgeServer) writeRegistry() error {
	server.registryMu.Lock()
	content, err := json.MarshalIndent(server.registry, "", "  ")
	server.registryMu.Unlock()
	if err != nil {
		return err
	}
	return atomicWriteFile(server.registryPath, content, 0600)
}

func (server *bridgeServer) Close() {
	server.closeOnce.Do(func() {
		_ = server.listener.Close()
		_ = os.Remove(server.socketPath)
		_ = os.Remove(server.registryPath)
	})
}

func detectBrowserFromParents() (string, string) {
	pid := os.Getppid()
	for depth := 0; depth < 8 && pid > 1; depth++ {
		output, err := exec.Command("/bin/ps", "-p", strconv.Itoa(pid), "-o", "ppid=,command=").Output()
		if err != nil {
			break
		}
		line := strings.TrimSpace(string(output))
		parts := strings.Fields(line)
		if len(parts) < 2 {
			break
		}
		parentPID, _ := strconv.Atoi(parts[0])
		command := strings.ToLower(strings.Join(parts[1:], " "))
		switch {
		case strings.Contains(command, "/arc.app/"):
			return "arc", "Arc"
		case strings.Contains(command, "/google chrome.app/"):
			return "chrome", "Google Chrome"
		case strings.Contains(command, "/chromium.app/"):
			return "chromium", "Chromium"
		case strings.Contains(command, "/microsoft edge.app/"):
			return "edge", "Microsoft Edge"
		case strings.Contains(command, "/brave browser.app/"):
			return "brave", "Brave"
		}
		pid = parentPID
	}
	return "chromium", "Chromium-compatible browser"
}

func logError(context string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "rep-host: %s: %v\n", context, err)
	}
}
