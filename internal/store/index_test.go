package store

import "testing"

func indexedRequest(id string) Request {
	return Request{ID: id, Method: "GET", URL: "https://example.test/", Response: &Response{Status: 200}}
}

func TestRequestIndexFullIDsNeverMatchAStoredShortPrefix(t *testing.T) {
	request := indexedRequest("h_abcd11111111")
	index := BuildIndex([]Request{request})
	for _, query := range []string{"h_abcd22222222", "abcd22222222", "h_abcd111111112222", "abcd_GET_404", "abcd_GET_200_extra", "abcd_BAD", "abc", ""} {
		if index.GetByAny(query) != nil || index.GetByID(query) != nil {
			t.Errorf("unrelated or malformed ID matched: %q", query)
		}
	}
	for _, query := range []string{request.ID, "abcd11111111", "abcd_GET_200", "abcd1", "h_abcd1"} {
		if got := index.GetByAny(query); got == nil || got.ID != request.ID {
			t.Errorf("expected unique request for %q: %+v", query, got)
		}
	}
	if index.GetExact("abcd1") != nil {
		t.Fatal("GetExact performed prefix lookup")
	}
}

func TestRequestIndexAmbiguousPrefixesAndSemanticAliasesFailClosed(t *testing.T) {
	requests := []Request{indexedRequest("h_abcd11111111"), indexedRequest("h_abcd22222222")}
	index := BuildIndex(requests)
	for _, query := range []string{"abcd", "h_abcd", "abcd_GET_200"} {
		if index.GetByAny(query) != nil || index.GetByID(query) != nil || index.GetExact(query) != nil {
			t.Errorf("ambiguous alias selected a request: %q", query)
		}
	}
	if index.GetBySemantic("abcd_GET_200") != nil {
		t.Fatal("semantic alias collision selected a request")
	}
	for _, request := range requests {
		if got := index.GetExact(request.ID); got == nil || got.ID != request.ID {
			t.Errorf("canonical ID lost to alias collision: %s", request.ID)
		}
	}
	if got := index.GetByAny("abcd2"); got == nil || got.ID != requests[1].ID {
		t.Fatal("a unique longer prefix should resolve")
	}
}

func TestRequestIndexExactIDWinsOverLongerStoredID(t *testing.T) {
	short := indexedRequest("h_abcd1234")
	long := indexedRequest("h_abcd123456789")
	index := BuildIndex([]Request{long, short})
	if got := index.GetByAny(short.ID); got == nil || got.ID != short.ID {
		t.Fatal("a stored exact ID lost to another request's prefix")
	}
}

func TestRequestIndexUnderscoredPrefixesPreserveAmbiguity(t *testing.T) {
	index := BuildIndex([]Request{indexedRequest("req_form_1234")})
	if got := index.GetByAny("req_form"); got == nil || got.ID != "req_form_1234" {
		t.Fatal("unique underscored original-ID prefix did not resolve")
	}
	second := indexedRequest("req_form_5678")
	index.Add(&second)
	if index.GetByAny("req_form") != nil {
		t.Fatal("ambiguous underscored prefix selected a request")
	}
	index = BuildIndex([]Request{
		indexedRequest("h_abcd11111111"),
		indexedRequest("h_abcd22222222"),
		indexedRequest("abcd_GET_200_extra"),
	})
	if index.GetByAny("abcd_GET_200") != nil {
		t.Fatal("ambiguous semantic alias resolved through an original-ID prefix")
	}
}

func TestRequestIndexReplacesAliasesForRepeatedCanonicalID(t *testing.T) {
	index := NewRequestIndex()
	first := indexedRequest("h_abcd11111111")
	index.Add(&first)
	updated := indexedRequest(first.ID)
	updated.Method = "POST"
	updated.Response.Status = 201
	index.Add(&updated)
	if index.GetBySemantic("abcd_GET_200") != nil || index.GetByAny("abcd_GET_200") != nil {
		t.Fatal("replaced request retained a stale semantic alias")
	}
	if got := index.GetByAny("abcd_POST_201"); got == nil || got != &updated {
		t.Fatal("updated semantic alias did not resolve")
	}
	index.Clear()
	if index.GetExact(first.ID) != nil || index.GetByAny("abcd") != nil || index.GetBySemantic("abcd_POST_201") != nil {
		t.Fatal("clear retained lookup aliases")
	}
}
