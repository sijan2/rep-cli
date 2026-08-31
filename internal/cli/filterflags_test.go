package cli

import (
	"testing"

	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

func TestRegisterFilterFlags_Defaults(t *testing.T) {
	var opts store.FilterOptions
	cmd := &cobra.Command{Use: "test"}
	RegisterFilterFlags(cmd, &opts, FilterFlagConfig{DefaultPrimary: true})

	// Cobra must know all the canonical flag names we promise agents.
	for _, name := range []string{"domain", "method", "status", "status-range", "url-pattern", "type", "primary", "limit", "offset"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing flag --%s", name)
		}
	}

	// ExcludeIgnored must default true so muted/ignored domains never sneak
	// into result sets.
	if !opts.ExcludeIgnored {
		t.Errorf("ExcludeIgnored should default to true")
	}

	// Primary default should be whatever the caller passed.
	flag := cmd.Flags().Lookup("primary")
	if flag.DefValue != "true" {
		t.Errorf("expected --primary default true, got %s", flag.DefValue)
	}
}

func TestRegisterFilterFlags_Skip(t *testing.T) {
	var opts store.FilterOptions
	cmd := &cobra.Command{Use: "test"}
	RegisterFilterFlags(cmd, &opts, FilterFlagConfig{
		SkipURLPattern: true,
		SkipOffset:     true,
	})

	if cmd.Flags().Lookup("url-pattern") != nil {
		t.Errorf("url-pattern flag should be skipped")
	}
	if cmd.Flags().Lookup("offset") != nil {
		t.Errorf("offset flag should be skipped")
	}
	// Non-skipped still present.
	if cmd.Flags().Lookup("domain") == nil {
		t.Errorf("domain flag incorrectly dropped")
	}
}

func TestParseCommaSeparated(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b ,c ", []string{"a", "b", "c"}},
		{",,", nil},
	}
	for _, tc := range cases {
		got := ParseCommaSeparated(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("ParseCommaSeparated(%q) len mismatch: got %v want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("ParseCommaSeparated(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}
