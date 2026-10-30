package commands

import (
	"fmt"

	"net/url"

	"strings"

	"github.com/bytedance/sonic"

	"github.com/repplus/rep-cli/internal/store"
)

type mutation struct {
	kind  string // "header" | "query" | "body"
	key   string // header name, query key, or body-path
	value string
}

func parseOneMutation(spec string) (mutation, error) {
	eq := strings.Index(spec, "=")
	if eq <= 0 {
		return mutation{}, fmt.Errorf("mutation %q must be path=value", spec)
	}
	path := spec[:eq]
	value := spec[eq+1:]
	dot := strings.Index(path, ".")
	if dot <= 0 {
		return mutation{}, fmt.Errorf("mutation path %q must be header.NAME / query.NAME / body.KEY", path)
	}
	kind := path[:dot]
	key := path[dot+1:]
	if key == "" {
		return mutation{}, fmt.Errorf("mutation path %q has empty key", path)
	}
	switch kind {
	case "header", "query", "body":
		return mutation{kind: kind, key: key, value: value}, nil
	default:
		return mutation{}, fmt.Errorf("unknown mutation kind %q (want header|query|body)", kind)
	}
}

func parseMatrixSpec(spec string) (string, []string, error) {
	eq := strings.Index(spec, "=")
	if eq <= 0 {
		return "", nil, fmt.Errorf("matrix %q must be KEY=[v1,v2,...]", spec)
	}
	key := spec[:eq]
	rest := strings.TrimSpace(spec[eq+1:])
	if !strings.HasPrefix(rest, "[") || !strings.HasSuffix(rest, "]") {
		return "", nil, fmt.Errorf("matrix values %q must be wrapped in [ ]", rest)
	}
	inner := strings.TrimSpace(rest[1 : len(rest)-1])
	if inner == "" {
		return "", nil, fmt.Errorf("matrix value list is empty")
	}
	raw := strings.Split(inner, ",")
	values := make([]string, 0, len(raw))
	for _, v := range raw {
		values = append(values, strings.TrimSpace(v))
	}
	// Validate key shape via parseOneMutation (with a dummy value).
	if _, err := parseOneMutation(key + "=x"); err != nil {
		return "", nil, err
	}
	return key, values, nil
}

// applyMutations returns a mutated copy; never touches the store's request.
func applyMutations(src *store.Request, muts []mutation) (*store.Request, error) {
	out := &store.Request{
		ID:      src.ID,
		Method:  src.Method,
		URL:     src.URL,
		Body:    src.Body,
		Headers: cloneHeaders(src.Headers),
	}
	if src.Response != nil {
		out.Response = src.Response
	}
	for _, m := range muts {
		switch m.kind {
		case "header":
			for name := range out.Headers {
				if strings.EqualFold(name, m.key) {
					delete(out.Headers, name)
				}
			}
			out.Headers[m.key] = []string{m.value}
		case "query":
			newURL, err := setQueryParam(out.URL, m.key, m.value)
			if err != nil {
				return nil, fmt.Errorf("query.%s: %w", m.key, err)
			}
			out.URL = newURL
		case "body":
			newBody, err := setBodyField(out.Body, store.HeaderFirst(out.Headers, "content-type"), m.key, m.value)
			if err != nil {
				return nil, fmt.Errorf("body.%s: %w", m.key, err)
			}
			out.Body = newBody
		}
	}
	return out, nil
}

func cloneHeaders(h store.HeaderMap) store.HeaderMap {
	out := make(store.HeaderMap, len(h))
	for k, v := range h {
		dup := make([]string, len(v))
		copy(dup, v)
		out[k] = dup
	}
	return out
}

func setQueryParam(raw, key, value string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func setBodyField(body, contentType, key, value string) (string, error) {
	ct := strings.ToLower(contentType)
	// urlencoded
	if strings.Contains(ct, "application/x-www-form-urlencoded") {
		vals, err := url.ParseQuery(body)
		if err != nil {
			return "", fmt.Errorf("parse urlencoded body: %w", err)
		}
		vals.Set(key, value)
		return vals.Encode(), nil
	}
	// JSON (default when ambiguous, per spec)
	if body == "" || strings.Contains(ct, "json") || looksLikeJSON(body) {
		var root interface{}
		if body == "" {
			root = map[string]interface{}{}
		} else if err := sonic.Unmarshal([]byte(body), &root); err != nil {
			return "", fmt.Errorf("parse JSON body: %w", err)
		}
		obj, ok := root.(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("JSON body is not an object; cannot set %q", key)
		}
		if err := setDotPath(obj, key, value); err != nil {
			return "", err
		}
		buf, err := sonic.Marshal(obj)
		if err != nil {
			return "", err
		}
		return string(buf), nil
	}
	return "", fmt.Errorf("unsupported body content-type %q for body mutation", contentType)
}

func looksLikeJSON(s string) bool {
	t := strings.TrimSpace(s)
	return strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")
}

func setDotPath(root map[string]interface{}, path, value string) error {
	parts := strings.Split(path, ".")
	cur := root
	for i, p := range parts {
		if p == "" {
			return fmt.Errorf("empty segment in path %q", path)
		}
		if i == len(parts)-1 {
			cur[p] = value
			return nil
		}
		next, ok := cur[p]
		if !ok {
			created := map[string]interface{}{}
			cur[p] = created
			cur = created
			continue
		}
		m, ok := next.(map[string]interface{})
		if !ok {
			return fmt.Errorf("path %q: segment %q is not an object", path, p)
		}
		cur = m
	}
	return nil
}

func originalReplayEvidence(req *store.Request) ([]byte, store.BodyCapture, error) {
	if req.Response == nil {
		return nil, store.BodyCapture{State: "unavailable", Reason: "response_not_captured"}, nil
	}
	var metadataCheck captureBodyCounts
	captureAccumulateBody(&metadataCheck, req, false)
	if metadataCheck.MetadataInvalid > 0 {
		return nil, store.BodyCapture{State: "unknown", Reason: "invalid_body_evidence"}, fmt.Errorf("captured body metadata is inconsistent")
	}
	body, _, evidence, err := bodyEvidence(req, false)
	if err != nil {
		// Corrupt encodings or metadata must not become a verified empty body.
		return nil, store.BodyCapture{State: "unknown", Reason: "invalid_body_evidence"}, err
	}
	return body, evidence, nil
}
