package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Caller interface {
	Call(ctx context.Context, method string, params any, out any) error
}

// Match narrows a Read to the entries the caller is waiting for.
type Match struct {
	URLRe, Method string
	Types         []string
	AdmittedOnly  bool
	Phase         string // start|end|any
}

func (m Match) wire() map[string]any {
	out := map[string]any{}
	if m.URLRe != "" {
		out["url_re"] = m.URLRe
	}
	if m.Method != "" {
		out["method"] = m.Method
	}
	if len(m.Types) > 0 {
		out["types"] = append([]string{}, m.Types...)
	}
	if m.AdmittedOnly {
		out["admitted_only"] = true
	}
	if m.Phase != "" {
		out["phase"] = m.Phase
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Client addresses one ledger on one tab. Every call carries Tab, ID and Owner
// so the extension can refuse a foreign caller with not_owned.
type Client struct {
	Caller    Caller
	Tab       int
	Owner, ID string
	MainFrame string
}

const (
	// MaxWait is the extension's long-poll ceiling.
	MaxWait = 10 * time.Second
	// readSlack covers transport time on top of the long-poll.
	readSlack = 5 * time.Second
	// maxBodyTotal is far above the 8 MiB admission cap; a larger total means a
	// corrupted or hostile response, not a body.
	maxBodyTotal = 64 << 20
)

// bodyPage is the per-call ceiling of browser.ledger.body; a variable so tests
// can page small bodies.
var bodyPage = 6 << 20

// Open creates the ledger with params from sitepolicy.LedgerParams, adding
// tab_id and owner. The ledger must be open before any navigation, which is
// why this is a separate step from reading.
func Open(ctx context.Context, c Caller, tab int, owner string, params map[string]any) (*Client, error) {
	if c == nil {
		return nil, errors.New("ledger: nil caller")
	}
	wire := make(map[string]any, len(params)+2)
	for k, v := range params {
		wire[k] = v
	}
	wire["tab_id"] = tab
	wire["owner"] = owner
	var out struct {
		LedgerID        string `json:"ledger_id"`
		Seq             uint64 `json:"seq"`
		MainFrameID     string `json:"main_frame_id"`
		Attached        bool   `json:"attached"`
		AlreadyAttached bool   `json:"already_attached"`
	}
	if err := c.Call(ctx, "browser.ledger.open", wire, &out); err != nil {
		return nil, err
	}
	if out.LedgerID == "" {
		return nil, errors.New("ledger: open returned no ledger_id")
	}
	return &Client{Caller: c, Tab: tab, Owner: owner, ID: out.LedgerID, MainFrame: out.MainFrameID}, nil
}

func (c *Client) base() map[string]any {
	return map[string]any{"tab_id": c.Tab, "ledger_id": c.ID, "owner": c.Owner}
}

// Read long-polls for entries after since. wait is capped at MaxWait; when the
// caller's context has no deadline one of wait + 5 s is applied so a stalled
// bridge cannot hang a run.
func (c *Client) Read(ctx context.Context, since uint64, m Match, wait time.Duration) (Batch, error) {
	if c.Caller == nil {
		return Batch{}, errors.New("ledger: nil caller")
	}
	if wait < 0 {
		wait = 0
	}
	if wait > MaxWait {
		wait = MaxWait
	}
	if _, has := ctx.Deadline(); !has {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, wait+readSlack)
		defer cancel()
	}
	params := c.base()
	params["since"] = since
	params["wait_ms"] = int(wait / time.Millisecond)
	if mw := m.wire(); mw != nil {
		params["match"] = mw
	}
	var b Batch
	if err := c.Caller.Call(ctx, "browser.ledger.read", params, &b); err != nil {
		return Batch{}, err
	}
	return b, nil
}

// Body fetches a complete body in bodyPage pieces and verifies the extension's
// sha256 over the concatenation, so a torn or substituted page cannot be
// decoded as the real response.
func (c *Client) Body(ctx context.Context, seq uint64) ([]byte, string, error) {
	if c.Caller == nil {
		return nil, "", errors.New("ledger: nil caller")
	}
	var buf []byte
	var want string
	total := -1
	for offset := 0; ; {
		params := c.base()
		params["seq"] = seq
		params["offset"] = offset
		params["length"] = bodyPage
		var out struct {
			Seq      uint64 `json:"seq"`
			Offset   int    `json:"offset"`
			Length   int    `json:"length"`
			Total    int    `json:"total"`
			SHA256   string `json:"sha256"`
			Encoding string `json:"encoding"`
			Data     string `json:"data"`
		}
		if err := c.Caller.Call(ctx, "browser.ledger.body", params, &out); err != nil {
			return nil, "", err
		}
		if out.Encoding != "base64" {
			return nil, "", fmt.Errorf("ledger: body seq %d: unsupported encoding %q", seq, out.Encoding)
		}
		if out.Offset != offset {
			return nil, "", fmt.Errorf("ledger: body seq %d: asked offset %d, got %d", seq, offset, out.Offset)
		}
		if out.Total < 0 || out.Total > maxBodyTotal {
			return nil, "", fmt.Errorf("ledger: body seq %d: implausible total %d", seq, out.Total)
		}
		if total < 0 {
			total = out.Total
			want = strings.ToLower(out.SHA256)
			buf = make([]byte, 0, total)
		} else if out.Total != total || !strings.EqualFold(out.SHA256, want) {
			return nil, "", fmt.Errorf("ledger: body seq %d changed while paging", seq)
		}
		chunk, err := base64.StdEncoding.DecodeString(out.Data)
		if err != nil {
			return nil, "", fmt.Errorf("ledger: body seq %d: %w", seq, err)
		}
		if offset+len(chunk) > total {
			return nil, "", fmt.Errorf("ledger: body seq %d: page overruns total %d", seq, total)
		}
		buf = append(buf, chunk...)
		offset += len(chunk)
		if offset >= total {
			break
		}
		if len(chunk) == 0 {
			return nil, "", fmt.Errorf("ledger: body seq %d: empty page at offset %d of %d", seq, offset, total)
		}
	}
	sum := sha256.Sum256(buf)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return nil, "", fmt.Errorf("ledger: body seq %d: sha256 mismatch (ledger %s, bytes %s)", seq, want, got)
	}
	return buf, got, nil
}

// Close is idempotent on the extension side; an unacknowledged close is still
// an error so a caller never believes a ledger is gone when it is not.
func (c *Client) Close(ctx context.Context) error {
	if c.Caller == nil {
		return errors.New("ledger: nil caller")
	}
	var out struct {
		Closed bool `json:"closed"`
	}
	if err := c.Caller.Call(ctx, "browser.ledger.close", c.base(), &out); err != nil {
		return err
	}
	if !out.Closed {
		return errors.New("ledger: close was not acknowledged")
	}
	return nil
}
