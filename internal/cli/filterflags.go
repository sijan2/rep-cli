// Package cli contains shared CLI wiring used across multiple subcommands.
// Its main job is a single source of truth for request-selection flags so
// every command that operates on a request set accepts the same grammar.
package cli

import (
	"strings"

	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

// FilterFlagConfig tweaks which flags get registered and their defaults.
// Zero value registers every common flag with primary=false as the default.
type FilterFlagConfig struct {
	// Skip disables individual flag categories. Use when a command owns the
	// name for another purpose (e.g. `search` uses its own pattern arg).
	SkipDomain       bool
	SkipMethod       bool
	SkipStatus       bool
	SkipURLPattern   bool
	SkipResourceType bool
	SkipPrimary      bool
	SkipLimit        bool
	SkipOffset       bool

	// DefaultPrimary is the initial value for --primary. Commands that
	// default-on (list) set true; commands that default-off (search) leave false.
	DefaultPrimary bool

	// TypeVar, if provided, is populated with the raw --type string. The caller
	// parses it into a []string via ParseCommaSeparated (kept in cmd package).
	// Pass nil to have this helper manage the storage internally; the caller
	// can read via the returned FilterFlagSet.
	TypeVar *string
}

// FilterFlagSet is returned by RegisterFilterFlags; callers read RawType after
// flag parsing to extract the resource-type list.
type FilterFlagSet struct {
	RawType string
}

// RegisterFilterFlags binds the common filter flags onto cmd, writing parsed
// values into opts. Call from the owning command's init(). After Cobra parses
// flags, the caller must post-process RawType into opts.ResourceTypes.
func RegisterFilterFlags(cmd *cobra.Command, opts *store.FilterOptions, cfg FilterFlagConfig) *FilterFlagSet {
	set := &FilterFlagSet{}

	if !cfg.SkipDomain {
		cmd.Flags().StringVarP(&opts.Domain, "domain", "d", "", "Filter by domain")
	}
	if !cfg.SkipMethod {
		cmd.Flags().StringVarP(&opts.Method, "method", "m", "", "Filter by HTTP method (GET, POST, PUT, DELETE, PATCH, OPTIONS)")
	}
	if !cfg.SkipStatus {
		cmd.Flags().IntVar(&opts.Status, "status", 0, "Filter by exact status code")
		cmd.Flags().StringVar(&opts.StatusRange, "status-range", "", "Filter by status range (2xx, 3xx, 4xx, 5xx)")
	}
	if !cfg.SkipURLPattern {
		cmd.Flags().StringVar(&opts.Pattern, "url-pattern", "", "Filter by URL pattern (regex)")
	}
	if !cfg.SkipResourceType {
		if cfg.TypeVar != nil {
			cmd.Flags().StringVar(cfg.TypeVar, "type", "", "Filter by resource type (comma-separated: script,xhr,fetch,document)")
		} else {
			cmd.Flags().StringVar(&set.RawType, "type", "", "Filter by resource type (comma-separated: script,xhr,fetch,document)")
		}
	}
	if !cfg.SkipPrimary {
		cmd.Flags().BoolVar(&opts.PrimaryOnly, "primary", cfg.DefaultPrimary, "Only requests to primary domains")
	}
	if !cfg.SkipLimit {
		cmd.Flags().IntVarP(&opts.Limit, "limit", "l", 0, "Limit number of results")
	}
	if !cfg.SkipOffset {
		cmd.Flags().IntVar(&opts.Offset, "offset", 0, "Skip first N results")
	}

	// Every filter-aware command excludes ignored domains by default; the
	// persistent-store ignore list is the intended noise filter.
	opts.ExcludeIgnored = true

	return set
}

// ParseCommaSeparated splits a comma-separated flag string into a trimmed
// list, discarding empty entries. Moved out of cmd so the helper avoids a
// cycle back into cmd.
func ParseCommaSeparated(input string) []string {
	if input == "" {
		return nil
	}
	parts := strings.Split(input, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
