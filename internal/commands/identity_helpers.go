package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/bytedance/sonic"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/store"
)

// AnonIdentity is the reserved principal that sends a request with every
// credential removed. It is never stored and cannot be overwritten.
const AnonIdentity = "anon"

// identityRecord binds a principal name to the credential material observed on
// a captured request. Secret bytes live here and are never printed: commands
// report a fingerprint so an agent can tell two principals apart without the
// credential ever entering its context.
type identityRecord struct {
	Name        string            `json:"name"`
	Headers     map[string]string `json:"headers"`
	Origin      string            `json:"origin,omitempty"`
	SourceID    string            `json:"source_request_id,omitempty"`
	CapturedAt  time.Time         `json:"captured_at"`
	Fingerprint string            `json:"fingerprint"`
}

// identityView is the redacted projection safe to emit to an agent.
type identityView struct {
	Name        string   `json:"name"`
	Headers     []string `json:"credential_headers"`
	Origin      string   `json:"origin,omitempty"`
	SourceID    string   `json:"source_request_id,omitempty"`
	CapturedAt  string   `json:"captured_at"`
	Fingerprint string   `json:"fingerprint"`
	Bytes       int      `json:"credential_bytes"`
}

// credentialHeaderNames are the headers lifted into an identity. Matching is
// case-insensitive; substring rules catch vendor-specific token headers.
var credentialHeaderExact = map[string]bool{
	"cookie": true, "authorization": true, "x-csrf-token": true,
	"x-xsrf-token": true, "x-api-key": true, "x-auth-token": true,
	"x-session-token": true, "x-access-token": true,
}

var credentialHeaderSubstrings = []string{"api-key", "apikey", "csrf-token", "auth-token", "session-token", "access-token"}

func isCredentialHeader(name string) bool {
	lower := strings.ToLower(name)
	if credentialHeaderExact[lower] {
		return true
	}
	for _, frag := range credentialHeaderSubstrings {
		if strings.Contains(lower, frag) {
			return true
		}
	}
	return false
}

func identityStorePath() (string, error) {
	dir, err := store.GetStorePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "identities.json"), nil
}

func loadIdentities() (map[string]identityRecord, error) {
	path, err := identityStorePath()
	if err != nil {
		return nil, err
	}
	buf, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]identityRecord{}, nil
		}
		return nil, err
	}
	out := map[string]identityRecord{}
	if err := sonic.Unmarshal(buf, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func saveIdentities(all map[string]identityRecord) error {
	path, err := identityStorePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	buf, err := sonic.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// fingerprintCredentials derives a short, stable, non-reversible label so an
// agent can distinguish principals and detect a rotated credential.
func fingerprintCredentials(headers map[string]string) string {
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, strings.ToLower(k))
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write([]byte(headers[n]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

func viewOf(rec identityRecord) identityView {
	names := make([]string, 0, len(rec.Headers))
	size := 0
	for k, v := range rec.Headers {
		names = append(names, strings.ToLower(k))
		size += len(v)
	}
	sort.Strings(names)
	return identityView{
		Name: rec.Name, Headers: names, Origin: rec.Origin,
		SourceID: rec.SourceID, CapturedAt: rec.CapturedAt.UTC().Format(time.RFC3339),
		Fingerprint: rec.Fingerprint, Bytes: size,
	}
}

// applyIdentity returns a copy of req sent as the named principal. The reserved
// name "anon" strips every credential header rather than substituting one.
func applyIdentity(src *store.Request, name string, all map[string]identityRecord) (*store.Request, error) {
	out := &store.Request{
		ID: src.ID, Method: src.Method, URL: src.URL, Body: src.Body,
		Headers: cloneHeaders(src.Headers), Response: src.Response,
	}
	// Remove whatever principal the capture already carried, so a partial
	// overwrite cannot leave two principals' credentials on one request.
	for key := range out.Headers {
		if isCredentialHeader(key) {
			delete(out.Headers, key)
		}
	}
	if strings.EqualFold(name, AnonIdentity) {
		return out, nil
	}
	rec, ok := all[name]
	if !ok {
		return nil, fmt.Errorf("no principal named %q (rep identity list)", name)
	}
	for key, value := range rec.Headers {
		out.Headers[key] = []string{value}
	}
	return out, nil
}

func originOf(rawURL string) string {
	if i := strings.Index(rawURL, "://"); i >= 0 {
		rest := rawURL[i+3:]
		if j := strings.IndexAny(rest, "/?#"); j >= 0 {
			return rawURL[:i+3] + rest[:j]
		}
		return rawURL
	}
	return ""
}
