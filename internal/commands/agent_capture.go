package commands

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/bridge"
	"github.com/repplus/rep-cli/internal/browserfast"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

type coreCaptureContext struct{}
type coreRecording struct {
	client           *bridge.Client
	tab              *agentstate.Tab
	handoff          browserCaptureHandoff
	sessionID, token string
	once             sync.Once
	result           map[string]any
	err              error
}

func beginCoreCapture(ctx context.Context, command *cobra.Command, client *bridge.Client, session *browserfast.Session, tab *agentstate.Tab, url string) (*coreRecording, error) {
	handoff, err := prepareBrowserCapture(ctx, client)
	if err != nil {
		return nil, err
	}
	var begun struct {
		ID    string `json:"session_id"`
		Token string `json:"capture_token"`
	}
	if err = session.Capture(ctx, url, &begun); err != nil {
		return nil, err
	}
	recording := &coreRecording{client: client, tab: tab, handoff: handoff, sessionID: begun.ID, token: begun.Token}
	command.SetContext(context.WithValue(command.Context(), coreCaptureContext{}, recording))
	return recording, nil
}
func (r *coreRecording) finish() {
	r.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var result map[string]any
		r.err = r.client.Call(ctx, "browser.capture.end", map[string]any{"session_id": r.sessionID, "capture_token": r.token, "owner": r.tab.Owner}, &result)
		if r.err != nil {
			return
		}
		var snapshot store.Export
		snapshot, r.err = readExactBrowserSnapshot(ctx, r.client, result, r.handoff.HostInstance, r.handoff.MaxSnapshotBytes)
		if r.err != nil {
			return
		}
		r.result, r.err = saveCoreCapture(r.tab, snapshot)
	})
}
func attachCoreCapture(command *cobra.Command, result *coreResult) {
	recording, ok := command.Context().Value(coreCaptureContext{}).(*coreRecording)
	if !ok {
		return
	}
	recording.finish()
	meta, ok := result.Meta.(map[string]any)
	if !ok {
		meta = map[string]any{}
		if result.Meta != nil {
			meta["operation_result"] = result.Meta
		}
	}
	result.Meta = meta
	if recording.err != nil {
		meta["capture_error"] = "capture evidence unavailable; input was not retried"
		result.Complete = boolPointer(false)
		result.Next = coreNext("doctor")
		return
	}
	meta["capture"] = recording.result
	if result.Next == "" {
		requests, _ := recording.result["requests"].([]map[string]any)
		apis := []string{}
		for _, request := range requests {
			if request["type"] == "xhr" {
				apis = append(apis, request["id"].(string))
			}
		}
		if len(apis) == 1 {
			result.Next = coreNext("body", apis[0])
		} else if recording.result["total"].(int) > 0 {
			result.Next = coreNext("net", recording.result["id"].(string))
		}
	}
}
func saveCoreCapture(tab *agentstate.Tab, snapshot store.Export) (map[string]any, error) {
	selected, _ := scope.Current()
	index, err := coreStore()
	if err != nil {
		return nil, err
	}
	id := agentstate.Token("s_")
	ids := make([]string, len(snapshot.Requests))
	preview := []map[string]any{}
	for i := range snapshot.Requests {
		request := &snapshot.Requests[i]
		request.OriginalID = request.ID
		request.ID = agentstate.Token("h_")
		ids[i] = request.ID
		if i < 12 {
			status := 0
			if request.Response != nil {
				status = request.Response.Status
			}
			preview = append(preview, map[string]any{"id": request.ID, "method": request.Method, "status": status, "type": coreRequestType(request)})
		}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(selected.DataDir, "captures", id+".json")
	if _, err = coreArtifact(path, ".json", encoded); err != nil {
		return nil, err
	}
	if err = index.RegisterCapture(id, tab.Owner, tab.Handle, path, ids); err != nil {
		return nil, err
	}
	complete := true
	if snapshot.BrowserSession != nil {
		s := snapshot.BrowserSession
		complete = !s.TimedOut && s.CaptureError == "" && s.DroppedRequests == 0 && len(s.CaptureWarnings) == 0
	}
	return map[string]any{"id": id, "requests": preview, "total": len(ids), "complete": complete}, nil
}
func loadCoreCapture(handle string) (agentstate.Capture, store.Export, error) {
	index, err := coreStore()
	if err != nil {
		return agentstate.Capture{}, store.Export{}, err
	}
	selected, _ := scope.Current()
	capture, err := index.Capture(handle, selected.Identity())
	if err != nil {
		return capture, store.Export{}, err
	}
	file, err := os.Open(capture.Archive)
	if err != nil {
		return capture, store.Export{}, agentstate.Failure("archive_unavailable", "retained capture file is unavailable", coreNext("scope"))
	}
	defer file.Close()
	var snapshot store.Export
	decoder := json.NewDecoder(io.LimitReader(file, browserSnapshotMaxBytes+1))
	err = decoder.Decode(&snapshot)
	if err != nil {
		return capture, snapshot, agentstate.Failure("archive_invalid", "retained capture could not be decoded", coreNext("scope"))
	}
	return capture, snapshot, nil
}
func coreCapturedRequest(handle string) (agentstate.Capture, *store.Request, error) {
	capture, snapshot, err := loadCoreCapture(handle)
	if err != nil {
		return capture, nil, err
	}
	for i := range snapshot.Requests {
		if snapshot.Requests[i].ID == handle {
			return capture, &snapshot.Requests[i], nil
		}
	}
	return capture, nil, agentstate.Failure("request_not_found", "request handle is absent from its exact archive", coreNext("net", capture.ID))
}
