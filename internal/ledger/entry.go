// Package ledger is the typed client of the extension's network ledger. It
// decodes what the page's own client loaded on a rep-owned tab and keeps the
// extension's redaction markers intact: a redacted value is never
// reconstructed, only its byte count and sha8 survive.
package ledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// HeaderValue is either a literal value or the extension's redaction marker
// {redacted:true, bytes, sha8}. Bytes and SHA8 are zero when the marker came
// without counters.
type HeaderValue struct {
	Value    string
	Redacted bool
	Bytes    int
	SHA8     string
}

type redactionMarker struct {
	Redacted bool   `json:"redacted"`
	Bytes    int    `json:"bytes,omitempty"`
	SHA8     string `json:"sha8,omitempty"`
}

// UnmarshalJSON accepts a JSON string or the marker object. Any other shape is
// an error rather than a silently empty value, so a wire change cannot make a
// credential field look blank.
func (v *HeaderValue) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		*v = HeaderValue{}
		return nil
	}
	switch b[0] {
	case '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*v = HeaderValue{Value: s}
		return nil
	case '{':
		var m redactionMarker
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
		if !m.Redacted {
			return errors.New("ledger: header value object is not a redaction marker")
		}
		*v = HeaderValue{Redacted: true, Bytes: m.Bytes, SHA8: m.SHA8}
		return nil
	}
	return fmt.Errorf("ledger: header value must be a string or redaction marker, got %q", b[0])
}

func (v HeaderValue) MarshalJSON() ([]byte, error) {
	if v.Redacted {
		return json.Marshal(redactionMarker{Redacted: true, Bytes: v.Bytes, SHA8: v.SHA8})
	}
	return json.Marshal(v.Value)
}

// String renders a redacted value as "redacted;bytes=N;sha8=X" so it stays
// visibly not-the-secret wherever headers are printed or compared.
func (v HeaderValue) String() string {
	if !v.Redacted {
		return v.Value
	}
	s := "redacted"
	if v.Bytes > 0 {
		s += ";bytes=" + strconv.Itoa(v.Bytes)
	}
	if v.SHA8 != "" {
		s += ";sha8=" + v.SHA8
	}
	return s
}

// Header is one [name, value] pair as the wire carries it.
type Header struct {
	Name  string
	Value HeaderValue
}

func (h *Header) UnmarshalJSON(b []byte) error {
	var pair []json.RawMessage
	if err := json.Unmarshal(b, &pair); err != nil {
		return fmt.Errorf("ledger: header must be a [name, value] pair: %w", err)
	}
	if len(pair) != 2 {
		return fmt.Errorf("ledger: header pair has %d elements", len(pair))
	}
	var name string
	if err := json.Unmarshal(pair[0], &name); err != nil {
		return fmt.Errorf("ledger: header name: %w", err)
	}
	var value HeaderValue
	if err := value.UnmarshalJSON(pair[1]); err != nil {
		return fmt.Errorf("ledger: header %s: %w", name, err)
	}
	*h = Header{Name: name, Value: value}
	return nil
}

func (h Header) MarshalJSON() ([]byte, error) {
	return json.Marshal([]any{h.Name, h.Value})
}

type Body struct {
	State    string `json:"state"`
	Reason   string `json:"reason,omitempty"`
	Bytes    int    `json:"bytes"`
	SHA256   string `json:"sha256,omitempty"`
	Encoding string `json:"encoding,omitempty"`
}

// Entry is one request the ledger saw. Its anonymous structs are untagged on
// purpose (they are part of the shared contract); the wire mapping lives in
// entryWire below.
type Entry struct {
	Seq                                             uint64
	Rev                                             int
	RequestID, LoaderID, FrameID, Type, Method, URL string
	Initiator                                       struct{ Type, URL string }
	Request                                         struct {
		Headers  []Header
		PostData HeaderValue
	}
	Response *struct {
		Status            int
		Mime, Protocol    string
		FromSW, FromCache bool
		Headers           []Header
	}
	Body  Body
	Error string
	T     struct{ Start, Response, End int64 }
}

type entryWire struct {
	Seq       uint64 `json:"seq"`
	Rev       int    `json:"rev"`
	RequestID string `json:"request_id"`
	LoaderID  string `json:"loader_id"`
	FrameID   string `json:"frame_id"`
	Type      string `json:"type"`
	Method    string `json:"method"`
	URL       string `json:"url"`
	Initiator struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"initiator"`
	Request struct {
		Headers  []Header    `json:"headers"`
		PostData HeaderValue `json:"post_data"`
	} `json:"request"`
	Response *struct {
		Status    int      `json:"status"`
		Mime      string   `json:"mime"`
		Protocol  string   `json:"protocol"`
		FromSW    bool     `json:"from_sw"`
		FromCache bool     `json:"from_cache"`
		Headers   []Header `json:"headers"`
	} `json:"response,omitempty"`
	Body  Body   `json:"body"`
	Error string `json:"error,omitempty"`
	T     struct {
		Start    int64 `json:"start"`
		Response int64 `json:"response,omitempty"`
		End      int64 `json:"end,omitempty"`
	} `json:"t"`
}

func (e *Entry) UnmarshalJSON(b []byte) error {
	var w entryWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	out := Entry{Seq: w.Seq, Rev: w.Rev, RequestID: w.RequestID, LoaderID: w.LoaderID, FrameID: w.FrameID, Type: w.Type, Method: w.Method, URL: w.URL, Body: w.Body, Error: w.Error}
	out.Initiator.Type, out.Initiator.URL = w.Initiator.Type, w.Initiator.URL
	out.Request.Headers, out.Request.PostData = w.Request.Headers, w.Request.PostData
	out.T.Start, out.T.Response, out.T.End = w.T.Start, w.T.Response, w.T.End
	if w.Response != nil {
		r := *w.Response
		out.Response = &struct {
			Status            int
			Mime, Protocol    string
			FromSW, FromCache bool
			Headers           []Header
		}{Status: r.Status, Mime: r.Mime, Protocol: r.Protocol, FromSW: r.FromSW, FromCache: r.FromCache, Headers: r.Headers}
	}
	*e = out
	return nil
}

func (e Entry) MarshalJSON() ([]byte, error) {
	w := entryWire{Seq: e.Seq, Rev: e.Rev, RequestID: e.RequestID, LoaderID: e.LoaderID, FrameID: e.FrameID, Type: e.Type, Method: e.Method, URL: e.URL, Body: e.Body, Error: e.Error}
	w.Initiator.Type, w.Initiator.URL = e.Initiator.Type, e.Initiator.URL
	w.Request.Headers, w.Request.PostData = e.Request.Headers, e.Request.PostData
	w.T.Start, w.T.Response, w.T.End = e.T.Start, e.T.Response, e.T.End
	if e.Response != nil {
		w.Response = &struct {
			Status    int      `json:"status"`
			Mime      string   `json:"mime"`
			Protocol  string   `json:"protocol"`
			FromSW    bool     `json:"from_sw"`
			FromCache bool     `json:"from_cache"`
			Headers   []Header `json:"headers"`
		}{Status: e.Response.Status, Mime: e.Response.Mime, Protocol: e.Response.Protocol, FromSW: e.Response.FromSW, FromCache: e.Response.FromCache, Headers: e.Response.Headers}
	}
	return json.Marshal(w)
}

// Header looks up a request header case-insensitively (signature detection
// reads request headers). See ResponseHeader for the other side.
func (e Entry) Header(name string) (HeaderValue, bool) {
	return lookup(e.Request.Headers, name)
}

func (e Entry) ResponseHeader(name string) (HeaderValue, bool) {
	if e.Response == nil {
		return HeaderValue{}, false
	}
	return lookup(e.Response.Headers, name)
}

func lookup(headers []Header, name string) (HeaderValue, bool) {
	for _, h := range headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value, true
		}
	}
	return HeaderValue{}, false
}

// HTTPHeader converts the response headers for governor.ParseRate. Response
// headers are allowlisted by the extension and never redacted; a marker that
// did arrive is rendered through HeaderValue.String, never as a value.
func (e Entry) HTTPHeader() http.Header {
	if e.Response == nil {
		return http.Header{}
	}
	return toHTTP(e.Response.Headers)
}

// RequestHTTPHeader keeps redacted headers present (as their marker text) so
// sitepolicy.HasSignature can see x-client-transaction-id without the value.
func (e Entry) RequestHTTPHeader() http.Header {
	return toHTTP(e.Request.Headers)
}

func toHTTP(headers []Header) http.Header {
	h := make(http.Header, len(headers))
	for _, hd := range headers {
		h.Add(hd.Name, hd.Value.String())
	}
	return h
}

type Nav struct {
	Seq          uint64 `json:"seq"`
	URL          string `json:"url"`
	SameDocument bool   `json:"same_document"`
	LoaderID     string `json:"loader_id"`
	FrameID      string `json:"frame_id"`
}

type Stats struct {
	Entries         int            `json:"entries"`
	Bodies          int            `json:"bodies"`
	BodyBytes       int            `json:"body_bytes"`
	Evicted         int            `json:"evicted"`
	SkippedByReason map[string]int `json:"skipped_by_reason"`
}

type Batch struct {
	Entries []Entry `json:"entries"`
	Navs    []Nav   `json:"navs"`
	NextSeq uint64  `json:"next_seq"`
	State   string  `json:"state"`
	Tab     struct {
		URL    string
		Active bool
	} `json:"tab"`
	Stats Stats `json:"stats"`
}
