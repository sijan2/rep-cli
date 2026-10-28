package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
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

var identityCmd = &cobra.Command{
	Use:   "identity",
	Short: "Bind captured credentials to named principals for differential testing",
	Long: `Keep credential material out of agent context while still testing as
several principals.

An authorization finding needs a named subject, a named resource, and an
observed denial. A status-code delta with no identity attached is not evidence.
'rep identity' stores the credential headers of a captured request under a
principal name; 'rep replay --as' re-executes a request as those principals.

Stored bytes are never printed. Commands report a fingerprint so two principals
can be told apart without the credential entering the transcript.

  rep identity capture <request-id> --as alice
  rep identity list
  rep replay <request-id> --as alice,bob,anon

The reserved principal 'anon' sends the request with every credential removed.`,
}

var identityCaptureName string

var identityCaptureCmd = &cobra.Command{
	Use:   "capture <request-id>",
	Short: "Bind a captured request's credentials to a principal name",
	Args:  cobra.ExactArgs(1),
	RunE:  runIdentityCapture,
}

var identityListCmd = &cobra.Command{
	Use:   "list",
	Short: "List principals and credential fingerprints (never the secrets)",
	Args:  cobra.NoArgs,
	RunE:  runIdentityList,
}

var identityForgetCmd = &cobra.Command{
	Use:   "forget <name>",
	Short: "Delete a stored principal",
	Args:  cobra.ExactArgs(1),
	RunE:  runIdentityForget,
}

func init() {
	rootCmd.AddCommand(identityCmd)
	identityCmd.AddCommand(identityCaptureCmd, identityListCmd, identityForgetCmd)
	identityCaptureCmd.Flags().StringVar(&identityCaptureName, "as", "", "Principal name to bind (required)")
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

func runIdentityCapture(cmd *cobra.Command, args []string) error {
	jsonMode := getOutputMode() == "json"
	name := strings.TrimSpace(identityCaptureName)
	if name == "" {
		ae := output.NewAgentError(output.ErrCodeInvalidArgument, "identity capture",
			"--as <name> is required",
			"rep identity capture "+args[0]+" --as alice")
		return output.EmitAgentError(os.Stdout, ae, jsonMode)
	}
	if strings.EqualFold(name, AnonIdentity) {
		ae := output.NewAgentError(output.ErrCodeInvalidArgument, "identity capture",
			"'anon' is reserved for the credential-free principal and cannot be bound",
			"choose another name, e.g. --as alice")
		return output.EmitAgentError(os.Stdout, ae, jsonMode)
	}

	req := lookupRequestForReplay(args[0])
	if req == nil {
		ae := output.NewAgentError(output.ErrCodeRequestNotFound, "identity capture",
			fmt.Sprintf("no request matched %q", args[0]),
			"rep list --limit 20  # browse available request IDs")
		return output.EmitAgentError(os.Stdout, ae, jsonMode)
	}

	creds := map[string]string{}
	for key, values := range req.Headers {
		if len(values) == 0 || !isCredentialHeader(key) {
			continue
		}
		creds[strings.ToLower(key)] = values[0]
	}
	if len(creds) == 0 {
		ae := output.NewAgentError(output.ErrCodeInvalidArgument, "identity capture",
			fmt.Sprintf("request %s carries no recognizable credential headers", req.ID),
			"capture a request made while authenticated as that principal",
			"rep detail "+req.ID+"  # inspect the headers that were actually sent")
		return output.EmitAgentError(os.Stdout, ae, jsonMode)
	}

	all, err := loadIdentities()
	if err != nil {
		return output.EmitAgentError(os.Stdout,
			output.WrapError(err, output.ErrCodeInternal, "identity capture"), jsonMode)
	}
	rec := identityRecord{
		Name: name, Headers: creds, Origin: originOf(req.URL),
		SourceID: req.ID, CapturedAt: time.Now().UTC(),
	}
	rec.Fingerprint = fingerprintCredentials(creds)
	all[name] = rec
	if err := saveIdentities(all); err != nil {
		return output.EmitAgentError(os.Stdout,
			output.WrapError(err, output.ErrCodeInternal, "identity capture"), jsonMode)
	}

	view := viewOf(rec)
	if jsonMode {
		env := output.WrapData("identity capture", "live.json", view)
		env.Suggest = []string{
			"rep identity capture <other-request-id> --as bob  # bind a second principal",
			"rep replay <request-id> --as " + name + ",anon  # differential authz test",
		}
		buf, err := sonic.MarshalIndent(env, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(buf))
		return nil
	}
	fmt.Printf("bound principal %q from %s\n", view.Name, view.SourceID)
	fmt.Printf("  credential headers: %s\n", strings.Join(view.Headers, ", "))
	fmt.Printf("  fingerprint: %s (%d credential bytes, not shown)\n", view.Fingerprint, view.Bytes)
	fmt.Printf("next: rep replay <request-id> --as %s,anon\n", view.Name)
	return nil
}

func runIdentityList(cmd *cobra.Command, args []string) error {
	jsonMode := getOutputMode() == "json"
	all, err := loadIdentities()
	if err != nil {
		return output.EmitAgentError(os.Stdout,
			output.WrapError(err, output.ErrCodeInternal, "identity list"), jsonMode)
	}
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)
	views := make([]identityView, 0, len(names))
	for _, n := range names {
		views = append(views, viewOf(all[n]))
	}

	if jsonMode {
		env := output.WrapData("identity list", "identities.json", map[string]interface{}{
			"identities": views,
			"reserved":   []string{AnonIdentity},
		})
		buf, err := sonic.MarshalIndent(env, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(buf))
		return nil
	}
	if len(views) == 0 {
		fmt.Println("no principals bound")
		fmt.Println("next: rep identity capture <request-id> --as alice")
		return nil
	}
	for _, v := range views {
		fmt.Printf("%-16s %s  [%s]  %d bytes  from %s\n",
			v.Name, v.Fingerprint, strings.Join(v.Headers, ","), v.Bytes, v.SourceID)
	}
	fmt.Printf("%-16s (reserved: sends no credentials)\n", AnonIdentity)
	return nil
}

func runIdentityForget(cmd *cobra.Command, args []string) error {
	jsonMode := getOutputMode() == "json"
	all, err := loadIdentities()
	if err != nil {
		return output.EmitAgentError(os.Stdout,
			output.WrapError(err, output.ErrCodeInternal, "identity forget"), jsonMode)
	}
	if _, ok := all[args[0]]; !ok {
		ae := output.NewAgentError(output.ErrCodeInvalidArgument, "identity forget",
			fmt.Sprintf("no principal named %q", args[0]), "rep identity list")
		return output.EmitAgentError(os.Stdout, ae, jsonMode)
	}
	delete(all, args[0])
	if err := saveIdentities(all); err != nil {
		return output.EmitAgentError(os.Stdout,
			output.WrapError(err, output.ErrCodeInternal, "identity forget"), jsonMode)
	}
	fmt.Printf("forgot principal %q\n", args[0])
	return nil
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
