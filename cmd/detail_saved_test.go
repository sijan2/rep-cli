package cmd

import (
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/repplus/rep-cli/internal/store"
)

func TestDetailSavedMetadataIsPinnedWhenLiveReusesRequestID(t *testing.T) {
	selected := summaryTestScope(t, "detail-test", "archive-metadata")
	requestID := "h_abcd11111111"
	archived := bodyLookupFixture(requestID, "archived-body")
	archived.URL = "https://example.test/archived"
	archive := store.Session{ID: "metadata-archive", Timestamp: 1, Requests: []store.Request{archived}}
	if err := store.AppendSessionLog(&archive); err != nil {
		t.Fatal(err)
	}
	writeSummaryCapture(t, selected.LivePath, store.Export{Requests: []store.Request{bodyLookupFixture(requestID, "live-body")}})
	oldMode, oldJSON, oldRaw := outputMode, jsonOutput, rawJSON
	oldSaved, _ := detailCmd.Flags().GetString("saved")
	t.Cleanup(func() {
		outputMode, jsonOutput, rawJSON = oldMode, oldJSON, oldRaw
		_ = detailCmd.Flags().Set("saved", oldSaved)
	})
	outputMode, jsonOutput, rawJSON = "meta", false, true
	if err := detailCmd.Flags().Set("saved", archive.HashID); err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = writer
	err = detailCmd.RunE(detailCmd, []string{requestID})
	os.Stdout = stdout
	_ = writer.Close()
	data, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || readErr != nil {
		t.Fatalf("detail failed: %v %v", err, readErr)
	}
	var out struct {
		URL      string `json:"url"`
		Body     string `json:"body"`
		Response struct {
			Body string `json:"body"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.URL != archived.URL || out.Body != "" || out.Response.Body != "" {
		t.Fatalf("detail used live metadata or included bodies: %+v", out)
	}
	_ = detailCmd.Flags().Set("saved", "missing-archive")
	if err := detailCmd.RunE(detailCmd, []string{requestID}); err == nil {
		t.Fatal("missing archive fell back to the live request")
	}
}
