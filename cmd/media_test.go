package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/store"
)

func mediaTestRecord() *store.Request {
	events := []store.StreamEvent{{Sequence: 1, Kind: "created"}, {Sequence: 2, Kind: "open"}}
	var total int64
	for i, data := range [][]byte{{0x1a, 0x45, 0xdf, 0xa3, 0xff, 0}, {0x80, 0, 0xc3, 0xa9}} {
		metadata, _ := json.Marshal(map[string]any{"chunk_index": i + 1, "mime_type": "video/webm;codecs=vp8"})
		events = append(events, store.StreamEvent{Sequence: int64(len(events) + 1), Kind: "chunk", Bytes: int64(len(data)), Payload: base64.StdEncoding.EncodeToString(data), PayloadEncoding: "base64", Metadata: metadata})
		total += int64(len(data))
	}
	events = append(events, store.StreamEvent{Sequence: int64(len(events) + 1), Kind: "closed", Metadata: json.RawMessage(`{"recorder_finalized":true,"chunk_count":2,"observed_media_bytes":10}`)})
	return &store.Request{ID: "h_media", RecordKind: "webrtc_media", Stream: &store.StreamCapture{
		Version: 1, Protocol: "webrtc_media", Source: "browser_media_recorder", State: "closed", PayloadSemantics: "reencoded_media", Clock: "performance_now_seconds",
		Metadata: json.RawMessage(`{"mime_type":"video/webm;codecs=vp8","media_kind":"video","direction":"sent","track_id":"track","parent_peer_id":"peer"}`), Events: events,
		Capture: store.StreamCoverage{State: "complete", Scope: "recorded_media_interval", CapturedEvents: int64(len(events)), ObservedEvents: int64(len(events)), CapturedBytes: total, ObservedBytes: total},
	}}
}

func TestMediaAssemblesExactBytesWithoutClobbering(t *testing.T) {
	r := mediaTestRecord()
	assembly, err := inspectMedia(r, true)
	if err != nil || !assembly.Complete || len(assembly.Chunks) != 2 {
		t.Fatalf("recording failed verification: %+v %v", assembly, err)
	}
	path := filepath.Join(t.TempDir(), "recording.webm")
	size, digest, err := saveMedia(path, assembly.Chunks)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x1a, 0x45, 0xdf, 0xa3, 0xff, 0, 0x80, 0, 0xc3, 0xa9}
	got, _ := os.ReadFile(path)
	sum := sha256.Sum256(want)
	info, _ := os.Stat(path)
	if !bytes.Equal(got, want) || size != int64(len(want)) || digest != hex.EncodeToString(sum[:]) || info.Mode().Perm() != 0600 {
		t.Fatalf("export bytes/hash/permissions changed: %x %d %s %v", got, size, digest, info.Mode())
	}
	if _, _, err := saveMedia(path, assembly.Chunks); err == nil {
		t.Fatal("existing output was overwritten")
	}
	link := filepath.Join(filepath.Dir(path), "link.webm")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := saveMedia(link, assembly.Chunks); err == nil {
		t.Fatal("output symlink was followed")
	}
}

func TestMediaRejectsMissingChunksAndConflictingContainers(t *testing.T) {
	r := mediaTestRecord()
	r.Stream.Events[3].Metadata = json.RawMessage(`{"chunk_index":3,"mime_type":"video/webm;codecs=vp8"}`)
	if _, err := inspectMedia(r, true); err == nil {
		t.Fatal("complete media accepted a missing container segment")
	}
	partial, err := inspectMedia(r, false)
	if err != nil || partial.Complete || len(partial.Reasons) == 0 {
		t.Fatalf("partial media lost missing chunk reason: %+v %v", partial, err)
	}
	r = mediaTestRecord()
	r.Stream.Events[3].Metadata = json.RawMessage(`{"chunk_index":1,"mime_type":"video/webm;codecs=vp8"}`)
	if _, err := inspectMedia(r, false); err == nil {
		t.Fatal("duplicate/reordered media chunk accepted")
	}
	r = mediaTestRecord()
	r.Stream.Events[3].Metadata = json.RawMessage(`{"chunk_index":2,"mime_type":"video/mp4"}`)
	if _, err := inspectMedia(r, false); err == nil {
		t.Fatal("conflicting containers were joined")
	}
}

func TestMediaCorruptionNeverPublishesArtifact(t *testing.T) {
	for _, invalid := range []string{"@invalid", "AA=="} {
		r := mediaTestRecord()
		r.Stream.Events[2].Payload = invalid
		assembly, err := inspectMedia(r, false)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.webm")
		if _, _, err := saveMedia(path, assembly.Chunks); err == nil {
			t.Fatal("invalid base64 or length was accepted")
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatal("failed export left published or temporary bytes")
		}
	}
}

func TestMediaRequiresRecorderFinalCounts(t *testing.T) {
	for _, metadata := range []string{
		`{}`, `null`, `{"recorder_finalized":true}`,
		`{"recorder_finalized":false,"chunk_count":2,"observed_media_bytes":10}`,
		`{"recorder_finalized":true,"chunk_count":3,"observed_media_bytes":10}`,
		`{"recorder_finalized":true,"chunk_count":2,"observed_media_bytes":11}`,
	} {
		t.Run(metadata, func(t *testing.T) {
			r := mediaTestRecord()
			r.Stream.Events[len(r.Stream.Events)-1].Metadata = json.RawMessage(metadata)
			if _, err := inspectMedia(r, true); err == nil {
				t.Fatal("missing final recorder confirmation was treated as complete")
			}
			assembly, err := inspectMedia(r, false)
			if err != nil || assembly.Complete || len(assembly.Reasons) == 0 {
				t.Fatalf("partial inspection lost the finalization issue: %+v %v", assembly, err)
			}
		})
	}
	r := mediaTestRecord()
	// Even internally consistent coverage counters cannot replace the recorder's
	// final count when its last container segment is missing from the archive.
	r.Stream.Events = append(r.Stream.Events[:3], r.Stream.Events[4])
	r.Stream.Events[3].Sequence = 4
	r.Stream.Capture.CapturedEvents, r.Stream.Capture.ObservedEvents = 4, 4
	r.Stream.Capture.CapturedBytes, r.Stream.Capture.ObservedBytes = 6, 6
	if _, err := inspectMedia(r, true); err == nil {
		t.Fatal("missing last chunk passed final recorder reconciliation")
	}
}

func TestMediaRejectsEncoderErrorClaims(t *testing.T) {
	for _, failure := range []store.StreamEvent{
		{Kind: "error", Reason: "media_recorder_error"},
		{Kind: "error"},
		{Kind: "stats", Error: "encoder failed"},
	} {
		r := mediaTestRecord()
		failure.Sequence = r.Stream.Events[1].Sequence
		r.Stream.Events[1] = failure
		if _, err := inspectMedia(r, true); err == nil {
			t.Fatalf("explicit encoder failure accepted as complete: %+v", failure)
		}
		assembly, err := inspectMedia(r, false)
		if err != nil || assembly.Complete || !strings.Contains(strings.Join(assembly.Reasons, ","), "missing_or_incomplete_chunks") {
			t.Fatalf("partial inspection lost encoder failure: %+v %v", assembly, err)
		}
	}
}

func TestMediaExportPinsLatestArchiveAndCoverage(t *testing.T) {
	evidenceStore, run := operationEvidenceFixture(t)
	r := mediaTestRecord()
	r.Stream.Events[len(r.Stream.Events)-1].Metadata = nil
	archive := store.Session{ID: "media-archive", Timestamp: 1000, Requests: []store.Request{*r}}
	if err := store.AppendSessionLog(&archive); err != nil {
		t.Fatal(err)
	}
	command := newMediaCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{r.ID, "--saved", "latest", "--save", filepath.Join(t.TempDir(), "media.webm")})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Saved    string `json:"saved"`
		Complete bool   `json:"assembly_complete"`
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || result.Saved != archive.HashID || result.Complete {
		t.Fatalf("export lost resolved archive or partial state: %s %v", output.Bytes(), err)
	}
	page, err := evidenceStore.ListOperations(run.ID, "", 20)
	if err != nil || len(page.Operations) != 2 {
		t.Fatalf("missing export evidence: %+v %v", page, err)
	}
	finished := page.Operations[1]
	if len(finished.ArtifactIDs) != 1 || len(finished.References) != 1 || finished.References[0].Metadata["saved"] != archive.HashID {
		t.Fatalf("export evidence retained mutable archive selector: %+v", finished)
	}
	artifact, err := evidenceStore.LoadArtifact(run.ID, finished.ArtifactIDs[0])
	if err != nil || artifact.Coverage.State != "partial" || artifact.Coverage.Reason == "" {
		t.Fatalf("derived artifact overstated coverage: %+v %v", artifact, err)
	}
}

func TestMediaPreviewRetainsSourceWithoutPayload(t *testing.T) {
	r := mediaTestRecord()
	r.ResponseBodyCapture = &store.BodyCapture{State: "not_applicable"}
	d := browserCapturedRequestDescriptors([]store.Request{*r})
	if len(d) != 1 || d[0].Stream == nil || d[0].Stream.Source != "browser_media_recorder" || d[0].BodyState != "not_applicable" {
		t.Fatalf("media preview lost native encoder source: %+v", d)
	}
	assembly, err := inspectMedia(r, true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := encodeMediaResult(mediaResult(r, assembly, "archive-hash"), 8192)
	if err != nil || strings.Contains(string(data), r.Stream.Events[2].Payload) || !strings.Contains(string(data), `"container_playability":"not_verified"`) {
		t.Fatalf("media metadata leaked payload or asserted playability: %s %v", data, err)
	}
}

func BenchmarkMediaAssembly(b *testing.B) {
	data := bytes.Repeat([]byte{0x80, 0xff, 0x1a, 0x45}, 64<<10)
	chunk := store.StreamEvent{Sequence: 1, Kind: "chunk", Bytes: int64(len(data)), Payload: base64.StdEncoding.EncodeToString(data), PayloadEncoding: "base64"}
	chunks := make([]store.StreamEvent, 16)
	for i := range chunks {
		chunks[i] = chunk
	}
	b.SetBytes(int64(len(data) * len(chunks)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := copyMedia(io.Discard, chunks); err != nil {
			b.Fatal(err)
		}
	}
}
