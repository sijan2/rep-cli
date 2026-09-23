package cmd

import (
	"testing"

	"github.com/repplus/rep-cli/internal/store"
)

func bodyLookupFixture(id, body string) store.Request {
	return store.Request{ID: id, Method: "GET", URL: "https://example.test/form", Response: &store.Response{Status: 200, Body: body}}
}

func TestBodyLookupArchivedExactIDWinsOverLivePrefixes(t *testing.T) {
	selected := summaryTestScope(t, "project", "agent")
	archivedID := "h_abcd11111111"
	if err := store.AppendSessionLog(&store.Session{ID: "archive", Timestamp: 1, Requests: []store.Request{bodyLookupFixture(archivedID, "archived-body")}}); err != nil {
		t.Fatal(err)
	}
	for _, liveID := range []string{"h_abcd22222222", archivedID + "2222"} {
		writeSummaryCapture(t, selected.LivePath, store.Export{Requests: []store.Request{bodyLookupFixture(liveID, "new-live-body")}})
		request, err := findBodyWebRequest(archivedID, "")
		if err != nil || request == nil || request.Response.Body != "archived-body" {
			t.Fatalf("archived full ID resolved to current traffic: %+v %v", request, err)
		}
		if request, err := findBodyWebRequest("abcd", ""); err != nil || request != nil {
			t.Fatalf("cross-source prefix ambiguity selected a request: %+v %v", request, err)
		}
		if request, err := findBodyWebRequest("abcd_GET_404", ""); err != nil || request != nil {
			t.Fatalf("wrong semantic label degraded to hash: %+v %v", request, err)
		}
	}
}

func TestBodySavedPinsArchiveWhenCanonicalIDsAreReused(t *testing.T) {
	selected := summaryTestScope(t, "project", "agent")
	requestID := "h_abcd11111111"
	archive := store.Session{ID: "archive", Timestamp: 1, Requests: []store.Request{bodyLookupFixture(requestID, "archived-body")}}
	if err := store.AppendSessionLog(&archive); err != nil {
		t.Fatal(err)
	}
	writeSummaryCapture(t, selected.LivePath, store.Export{Requests: []store.Request{bodyLookupFixture(requestID, "live-body"), bodyLookupFixture("h_ffff11111111", "live-only")}})
	for _, saved := range []string{archive.ID, archive.HashID, "latest"} {
		request, err := findBodyWebRequest(requestID, saved)
		if err != nil || request == nil || request.Response.Body != "archived-body" {
			t.Fatalf("pinned archive read wrong source: %+v %v", request, err)
		}
		request, err = findBodyWebRequest("h_ffff11111111", saved)
		if err != nil || request != nil {
			t.Fatalf("pinned archive fell back to live: %+v %v", request, err)
		}
	}
	request, err := findBodyWebRequest(requestID, "")
	if err != nil || request == nil || request.Response.Body != "live-body" {
		t.Fatalf("canonical live ID did not read live: %+v %v", request, err)
	}
	if _, err := findBodyWebRequest(requestID, "missing"); err == nil {
		t.Fatal("missing explicit archive did not fail closed")
	}
}

func TestArchivedRequestPrefixesMustBeUniqueAcrossSessions(t *testing.T) {
	persistent := store.NewStore()
	persistent.Sessions = []store.Session{
		{ID: "first", Requests: []store.Request{bodyLookupFixture("h_abcd11111111", "one")}},
		{ID: "second", Requests: []store.Request{bodyLookupFixture("h_abcd22222222", "two")}},
	}
	if request := findRequestByAnyID(persistent, "abcd"); request != nil {
		t.Fatalf("archive prefix picked the first session: %+v", request)
	}
	if request := findRequestByAnyID(persistent, "abcd_GET_200"); request != nil {
		t.Fatalf("semantic collision picked an archive: %+v", request)
	}
	if request := findRequestByAnyID(persistent, "h_abcd22222222"); request == nil || request.Response.Body != "two" {
		t.Fatalf("exact archived ID failed: %+v", request)
	}
}
