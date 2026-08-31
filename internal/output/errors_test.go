package output

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestFormatErrorText_Basic(t *testing.T) {
	ae := NewAgentError(
		ErrCodeRequestNotFound,
		"curl",
		"no request matched \"abcd\"",
		"rep list --line | head",
		"rep summary",
	)
	out := FormatErrorText(ae)

	for _, want := range []string{
		`error: no request matched "abcd"`,
		"code: request_not_found",
		"command: curl",
		"suggest:",
		"rep list --line | head",
		"rep summary",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, out)
		}
	}
}

func TestWrapError_PreservesChain(t *testing.T) {
	base := errors.New("disk read failed")
	wrapped := fmt.Errorf("store.json load: %w", base)
	outer := fmt.Errorf("live lookup: %w", wrapped)

	ae := WrapError(outer, ErrCodeStoreRead, "list")
	if len(ae.Chain) != 3 {
		t.Fatalf("expected 3-level chain, got %d: %v", len(ae.Chain), ae.Chain)
	}
	if ae.Chain[0] != outer.Error() {
		t.Errorf("chain[0] wrong: %q", ae.Chain[0])
	}
	if ae.Chain[2] != base.Error() {
		t.Errorf("chain[2] wrong: %q", ae.Chain[2])
	}

	rendered := FormatErrorText(ae)
	if !strings.Contains(rendered, "caused_by:") {
		t.Errorf("expected caused_by block for multi-level chain:\n%s", rendered)
	}
}

func TestWrapError_NilError(t *testing.T) {
	ae := WrapError(nil, ErrCodeInternal, "list", "retry")
	if ae.Message != "" {
		t.Errorf("nil error should produce empty message, got %q", ae.Message)
	}
	if ae.Code != ErrCodeInternal {
		t.Errorf("code not set")
	}
	if len(ae.Suggest) != 1 {
		t.Errorf("suggest not preserved")
	}
}
