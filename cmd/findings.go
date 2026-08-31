package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

// findings is a companion view for `rep note`. Where `rep note` (no args)
// prints notes chronologically, `rep findings` groups them by referenced
// request ID — the shape an agent actually wants when re-orienting on a
// capture it's already poked at. Survives across turns (same backing store).

var (
	findingsRequestFilter string
	findingsLimit         int
	findingsTagFilter     string
)

var findingsCmd = &cobra.Command{
	Use:   "findings",
	Short: "Print persisted notes grouped by request ID",
	Long: `List the notes saved via 'rep note', grouped by request-id
reference. Notes without a --ref are collected under "(unscoped)".

Use this at the start of each turn to recall what's already been tried on a
capture without re-scanning everything.

Examples:
  rep findings                     # all, grouped by ref
  rep findings --request a75b      # only one request's notes
  rep findings --tag idor          # only notes tagged 'idor'
  rep findings --limit 10 -j       # last 10, JSON envelope`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ns, err := store.LoadNotes()
		if err != nil {
			ae := output.WrapError(err, output.ErrCodeStoreRead, "findings",
				"rep note \"first observation\"  # create the log")
			return output.EmitAgentError(os.Stdout, ae, getOutputMode() == "json")
		}

		entries := ns.Notes

		if findingsTagFilter != "" {
			kept := entries[:0]
			for _, n := range entries {
				for _, t := range n.Tags {
					if t == findingsTagFilter {
						kept = append(kept, n)
						break
					}
				}
			}
			entries = kept
		}
		if findingsRequestFilter != "" {
			kept := entries[:0]
			for _, n := range entries {
				for _, r := range n.Refs {
					if r == findingsRequestFilter ||
						strings.HasPrefix(r, "h_"+findingsRequestFilter) ||
						strings.HasPrefix(r, findingsRequestFilter) {
						kept = append(kept, n)
						break
					}
				}
			}
			entries = kept
		}
		if findingsLimit > 0 && len(entries) > findingsLimit {
			entries = entries[len(entries)-findingsLimit:]
		}

		// Group by first ref (fallback: "(unscoped)").
		groups := map[string][]store.Note{}
		order := []string{}
		for _, n := range entries {
			key := "(unscoped)"
			if len(n.Refs) > 0 {
				key = n.Refs[0]
			}
			if _, seen := groups[key]; !seen {
				order = append(order, key)
			}
			groups[key] = append(groups[key], n)
		}
		sort.Strings(order)

		if getOutputMode() == "json" {
			payload := map[string]interface{}{
				"total":  len(entries),
				"groups": buildFindingGroups(order, groups),
			}
			if useEnvelope() {
				env := output.WrapData("findings", "notes.jsonl", payload)
				filters := map[string]interface{}{}
				if findingsRequestFilter != "" {
					filters["request"] = findingsRequestFilter
				}
				if findingsTagFilter != "" {
					filters["tag"] = findingsTagFilter
				}
				if len(filters) > 0 {
					env.Filters = filters
				}
				out, _ := sonic.MarshalIndent(env, "", "  ")
				fmt.Println(string(out))
				return nil
			}
			out, _ := sonic.MarshalIndent(payload, "", "  ")
			fmt.Println(string(out))
			return nil
		}

		if len(entries) == 0 {
			fmt.Println("no findings yet")
			fmt.Println("  start with: rep note --ref <id> \"hypothesis\"")
			return nil
		}

		fmt.Printf("%d findings across %d request(s)\n\n", len(entries), len(order))
		for _, key := range order {
			fmt.Printf("# %s\n", key)
			for _, n := range groups[key] {
				stamp := n.Timestamp.Format("2006-01-02 15:04")
				if len(n.Tags) > 0 {
					fmt.Printf("  [%s] (%s) %s\n", stamp, strings.Join(n.Tags, ","), n.Content)
				} else {
					fmt.Printf("  [%s] %s\n", stamp, n.Content)
				}
			}
			fmt.Println()
		}
		return nil
	},
}

type findingGroup struct {
	Ref   string       `json:"ref"`
	Notes []store.Note `json:"notes"`
}

func buildFindingGroups(order []string, groups map[string][]store.Note) []findingGroup {
	out := make([]findingGroup, 0, len(order))
	for _, k := range order {
		out = append(out, findingGroup{Ref: k, Notes: groups[k]})
	}
	return out
}

func init() {
	rootCmd.AddCommand(findingsCmd)
	findingsCmd.Flags().StringVar(&findingsRequestFilter, "request", "", "Filter to one request ID or prefix")
	findingsCmd.Flags().IntVar(&findingsLimit, "limit", 0, "Return only the last N entries")
	findingsCmd.Flags().StringVar(&findingsTagFilter, "tag", "", "Filter by tag")
}
