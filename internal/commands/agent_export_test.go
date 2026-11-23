package commands

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/store"
)

func TestExportCapturedCredentialsAndBinaryBytes(t *testing.T) {
	req := netRequest("/api?token=original-query", base64.StdEncoding.EncodeToString([]byte{0, 255, 1, 2}))
	req.Method = "POST"
	req.Headers = store.HeaderMap{"Cookie": {"sid=original-cookie"}, "Authorization": {"Bearer original-token"}, "X-CSRF-Token": {"original-csrf"}, "Content-Type": {"application/octet-stream"}}
	req.Body = base64.StdEncoding.EncodeToString([]byte{3, 0, 254, 4})
	req.RequestBodyCapture = &store.BodyCapture{State: "complete", Encoding: "base64", CapturedBytes: 4}
	req.ResponseEncoding = "base64"
	req.ResponseBodyCapture = &store.BodyCapture{State: "complete", CapturedBytes: 4}
	req.Response.Headers["Set-Cookie"] = []string{"one=original-one", "two=original-two"}
	capture, snapshot := netFixture(t, []store.Request{req})
	cmd := newCoreExportCommand()
	cmd.SetContext(context.Background())
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	path := filepath.Join(t.TempDir(), "request.json")
	if err := coreExport(cmd, snapshot.Requests[0].ID, path, "json"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		SourceArchive             string        `json:"source_archive"`
		Complete                  bool          `json:"complete"`
		Record                    store.Request `json:"record"`
		RequestBody, ResponseBody exportedBody
	}
	// Underscored wire fields need explicit decoding so byte identity is checked.
	var wire map[string]json.RawMessage
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(data, &wire)
	_ = json.Unmarshal(wire["request_body"], &saved.RequestBody)
	_ = json.Unmarshal(wire["response_body"], &saved.ResponseBody)
	if saved.SourceArchive != capture.ID || !saved.Complete || saved.Record.URL != req.URL || saved.Record.Headers["Cookie"][0] != "sid=original-cookie" || len(saved.Record.Response.Headers["Set-Cookie"]) != 2 {
		t.Fatalf("lost raw metadata: %s", data)
	}
	if saved.RequestBody.Data != req.Body || saved.ResponseBody.Data != req.Response.Body {
		t.Fatal("body bytes changed")
	}
	for _, body := range []exportedBody{saved.RequestBody, saved.ResponseBody} {
		decoded, err := base64.StdEncoding.DecodeString(body.Data)
		if err != nil || len(decoded) != 4 || body.Capture.State != "complete" {
			t.Fatal(body, err)
		}
	}
	if strings.Contains(stdout.String(), "original-") {
		t.Fatal("raw values belong in explicit file, not result metadata")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	if err = coreExport(cmd, snapshot.Requests[0].ID, path, "json"); err == nil {
		t.Fatal("overwrote export")
	}
	if err = coreExport(nil, "invalid", "", "json"); err == nil {
		t.Fatal("blank destination accepted")
	}
}

func TestCurlExportQuotesArgumentsAndStreamsExactBody(t *testing.T) {
	body := bytes.Repeat([]byte{0, 255, '\n', '\'', '$'}, 30000)
	req := netRequest("/api[x]?q='$(printf injected)'", "{}")
	req.Method = "POST"
	req.Headers = store.HeaderMap{"Cookie": {"sid='$(printf injected)'"}, "Authorization": {"Bearer exact"}, "X-Repeat": {"one", "two"}, "X-Empty": {""}, "Content-Length": {"9999"}}
	exported := exportedBody{Data: base64.StdEncoding.EncodeToString(body), Capture: store.BodyCapture{State: "complete"}}
	script, err := exportCurl(&req, exported)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// This stub is not a network client. It reports shell arguments and staged bytes.
	stub := "#!/usr/bin/env python3\nimport sys,json,base64,pathlib\nargs=sys.argv[1:]\npayload=pathlib.Path(args[args.index('--data-binary')+1][1:]).read_bytes()\nprint(json.dumps({'argv':args,'body':base64.b64encode(payload).decode()}))\n"
	if err = os.WriteFile(filepath.Join(dir, "curl"), []byte(stub), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "request.sh")
	_ = os.WriteFile(path, script, 0600)
	cmd := exec.Command("sh", path)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	result, err := cmd.Output()
	if err != nil {
		t.Fatal(err, string(script))
	}
	var seen struct {
		Argv []string `json:"argv"`
		Body string   `json:"body"`
	}
	if err = json.Unmarshal(result, &seen); err != nil {
		t.Fatal(err)
	}
	if seen.Body != exported.Data {
		t.Fatal("binary request body changed")
	}
	all := strings.Join(seen.Argv, "\n")
	for _, exact := range []string{req.URL, "Cookie: sid='$(printf injected)'", "Authorization: Bearer exact", "X-Repeat: one", "X-Repeat: two", "X-Empty;"} {
		if !strings.Contains(all, exact) {
			t.Fatalf("missing literal %q: %s", exact, all)
		}
	}
	for i, arg := range seen.Argv {
		if arg == "--data-binary" {
			if _, err := os.Stat(strings.TrimPrefix(seen.Argv[i+1], "@")); !os.IsNotExist(err) {
				t.Fatal("temporary body was not cleaned up")
			}
		}
	}
	if strings.Contains(all, "Content-Length") {
		t.Fatal("stale body length retained")
	}
	exported.Capture.State = "partial"
	if _, err = exportCurl(&req, exported); err == nil {
		t.Fatal("partial request became runnable")
	}
}

func TestExportPreservesInvalidEvidenceAsInvalid(t *testing.T) {
	req := netRequest("/partial", "not base64!")
	req.ResponseEncoding = "base64"
	_, snapshot := netFixture(t, []store.Request{req})
	path := filepath.Join(t.TempDir(), "partial.json")
	cmd := newCoreExportCommand()
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := coreExport(cmd, snapshot.Requests[0].ID, path, "json"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !bytes.Contains(data, []byte("not base64!")) || !bytes.Contains(data, []byte(`"state": "invalid"`)) || !bytes.Contains(data, []byte(`"complete": false`)) {
		t.Fatal(string(data))
	}
}

func TestCurlExportDoesNotDispatchAfterBodyDecodeFailure(t *testing.T) {
	dir := t.TempDir()
	req := netRequest("/write", "")
	req.Method = "POST"
	script, err := exportCurl(&req, exportedBody{Data: "AAE=", Capture: store.BodyCapture{State: "complete"}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "request.sh")
	marker := filepath.Join(dir, "curl-called")
	if err = os.WriteFile(path, script, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "python3"), []byte("#!/bin/sh\nexit 23\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "curl"), []byte("#!/bin/sh\n"+quoteCommand("touch", marker)+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", path)
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "TMPDIR="+dir)
	if err = cmd.Run(); err == nil {
		t.Fatal("body generation failed without failing script")
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("curl was dispatched after producer failure")
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "rep-export.*"))
	if len(leftovers) != 0 {
		t.Fatal("temporary body leaked", leftovers)
	}
}

func TestCurlExportHTTP2AndHead(t *testing.T) {
	req := netRequest("/document", "")
	req.Method = "HEAD"
	req.Headers = store.HeaderMap{":method": {"HEAD"}, ":scheme": {"https"}, ":path": {"/document"}, ":authority": {"fixture.test"}, "Cookie": {"session=exact"}}
	script, err := exportCurl(&req, exportedBody{Capture: store.BodyCapture{State: "not_applicable"}})
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	if !strings.Contains(text, "--head") || !strings.Contains(text, "--request HEAD") || !strings.Contains(text, "Cookie: session=exact") || strings.Contains(text, "--header ':method") || strings.Contains(text, "--data-binary") {
		t.Fatal(text)
	}
}
