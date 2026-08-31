package main

import "testing"

func resetHostState() {
	liveData = &LiveData{Version: "2.0", Requests: []Request{}}
	hostBrowser = "arc"
	browserCaptureActive = false
	browserCaptureSealed = false
}

func TestSessionCaptureIsIsolatedFromAmbientWebRequestTraffic(t *testing.T) {
	resetHostState()
	handleMessage(&Message{Action: "session_begin", SessionID: "navigate-test", URL: "https://github.com", CaptureMode: "navigate"})
	handleMessage(&Message{Action: "add", Request: &Request{ID: "ambient-during", Method: "POST", URL: "https://chatgpt.com/telemetry", CaptureSource: "webrequest"}})
	handleMessage(&Message{Action: "add", Request: &Request{ID: "target", Method: "GET", URL: "https://github.com", CaptureSource: "cdp"}})
	handleMessage(&Message{Action: "session_end", URL: "https://github.com"})
	handleMessage(&Message{Action: "add", Request: &Request{ID: "ambient-after", Method: "POST", URL: "https://mail.google.com/sync", CaptureSource: "webrequest"}})

	if len(liveData.Requests) != 1 || liveData.Requests[0].ID != "target" {
		t.Fatalf("session was contaminated: %+v", liveData.Requests)
	}
	if liveData.Browser == nil || liveData.Browser.Browser != "arc" || liveData.Browser.FinishedAt == "" {
		t.Fatalf("browser session metadata incomplete: %+v", liveData.Browser)
	}
}

func TestAmbientBeginUnsealsWebRequestCapture(t *testing.T) {
	resetHostState()
	browserCaptureSealed = true
	handleMessage(&Message{Action: "ambient_begin", SessionID: "ambient-test"})
	handleMessage(&Message{Action: "add", Request: &Request{ID: "normal", Method: "GET", URL: "https://example.com", CaptureSource: "webrequest"}})
	if len(liveData.Requests) != 1 || liveData.Requests[0].ID != "normal" {
		t.Fatalf("ambient request not captured: %+v", liveData.Requests)
	}
}

func TestAmbientResumePreservesUnfinishedSession(t *testing.T) {
	resetHostState()
	handleMessage(&Message{Action: "ambient_begin", SessionID: "ambient-existing"})
	handleMessage(&Message{Action: "add", Request: &Request{ID: "before-restart", Method: "GET", URL: "https://example.com", CaptureSource: "webrequest"}})
	handleMessage(&Message{Action: "ambient_resume"})

	if liveData.SessionID != "ambient-existing" || len(liveData.Requests) != 1 {
		t.Fatalf("ambient resume replaced the live session: %+v", liveData)
	}
	if browserCaptureActive || browserCaptureSealed {
		t.Fatalf("ambient resume left capture gated: active=%v sealed=%v", browserCaptureActive, browserCaptureSealed)
	}
}

func TestAmbientResumeStartsAfterFinishedSession(t *testing.T) {
	resetHostState()
	handleMessage(&Message{Action: "ambient_begin", SessionID: "ambient-old"})
	handleMessage(&Message{Action: "ambient_end"})
	handleMessage(&Message{Action: "ambient_resume", SessionID: "ambient-new"})

	if liveData.SessionID != "ambient-new" || liveData.Browser == nil || liveData.Browser.FinishedAt != "" {
		t.Fatalf("ambient resume did not start a fresh session: %+v", liveData)
	}
}
