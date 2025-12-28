package cmd

import (
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	muteRemove bool
	muteClear  bool
	muteList   bool
)

var muteCmd = &cobra.Command{
	Use:   "mute [domain/path...]",
	Short: "Mute specific endpoints to reduce noise",
	Long: `Mute specific URL paths to filter them from output.

Unlike 'ignore' which blocks entire domains, 'mute' lets you silence
specific noisy endpoints while keeping the rest of the domain visible.
Perfect for endpoints like /log, /health, or /telemetry that flood output.

Pattern formats:
  domain/path          Mute exact path on domain
  domain/path*         Mute paths starting with prefix
  domain/^regex$       Mute paths matching regex
  */path               Mute path on ALL domains

Examples:
  rep mute example.com/log                     Mute /log endpoint
  rep mute example.com/api/v1/telemetry        Mute specific API path
  rep mute "example.com/health*"               Mute /health, /healthz, /healthcheck
  rep mute "*/log"                             Mute /log on all domains
  rep mute "example.com/^/api/v[0-9]+/log"     Mute with regex
  rep mute --remove example.com/log            Unmute a path
  rep mute --list                              Show all muted paths
  rep mute --clear                             Clear all muted paths`,
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := store.Get()
		if err != nil {
			return fmt.Errorf("failed to load store: %w", err)
		}

		// List mode
		if muteList || len(args) == 0 && !muteClear {
			muted := s.GetMutedPaths()
			if getOutputMode() == "json" {
				out, _ := sonic.MarshalIndent(muted, "", "  ")
				fmt.Println(string(out))
			} else {
				if len(muted) == 0 {
					pterm.Info.Println("No muted paths. Use 'rep mute domain/path' to add.")
				} else {
					pterm.DefaultSection.Println("Muted Paths")
					for _, mp := range muted {
						if mp.Domain == "*" {
							fmt.Printf("  *%s\n", mp.Pattern)
						} else {
							fmt.Printf("  %s%s\n", mp.Domain, mp.Pattern)
						}
					}
					fmt.Printf("\nTotal: %d muted paths\n", len(muted))
					fmt.Println("\nUse --remove to unmute, --clear to clear all")
				}
			}
			return nil
		}

		// Clear mode
		if muteClear {
			count := s.ClearMutedPaths()
			if err := s.Save(); err != nil {
				return fmt.Errorf("failed to save: %w", err)
			}
			if getOutputMode() == "json" {
				out, _ := sonic.MarshalIndent(map[string]interface{}{
					"action":  "clear",
					"removed": count,
				}, "", "  ")
				fmt.Println(string(out))
			} else {
				pterm.Success.Printf("Cleared all muted paths (%d removed)\n", count)
			}
			return nil
		}

		// Remove mode
		if muteRemove {
			removed := 0
			for _, pattern := range args {
				if s.Unmute(pattern) {
					removed++
				}
			}
			if err := s.Save(); err != nil {
				return fmt.Errorf("failed to save: %w", err)
			}
			if getOutputMode() == "json" {
				out, _ := sonic.MarshalIndent(map[string]interface{}{
					"action":  "remove",
					"patterns": args,
					"removed": removed,
				}, "", "  ")
				fmt.Println(string(out))
			} else {
				pterm.Success.Printf("Unmuted %d path(s)\n", removed)
			}
			return nil
		}

		// Add mode (default)
		added := 0
		for _, pattern := range args {
			if s.Mute(pattern) {
				added++
			}
		}
		if err := s.Save(); err != nil {
			return fmt.Errorf("failed to save: %w", err)
		}

		if getOutputMode() == "json" {
			out, _ := sonic.MarshalIndent(map[string]interface{}{
				"action":   "add",
				"patterns": args,
				"added":    added,
				"total":    len(s.GetMutedPaths()),
			}, "", "  ")
			fmt.Println(string(out))
		} else {
			pterm.Success.Printf("Muted %d path(s)\n", added)
			muted := s.GetMutedPaths()
			if len(muted) > 0 {
				pterm.Info.Printf("Total muted: %d paths\n", len(muted))
			}
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(muteCmd)
	muteCmd.Flags().BoolVar(&muteRemove, "remove", false, "Remove paths from mute list")
	muteCmd.Flags().BoolVar(&muteClear, "clear", false, "Clear all muted paths")
	muteCmd.Flags().BoolVar(&muteList, "list", false, "List all muted paths")
}
