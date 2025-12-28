package cmd

import (
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	primaryRemove bool
	primaryClear  bool
)

var primaryCmd = &cobra.Command{
	Use:   "primary [domain...]",
	Short: "Manage primary target domains",
	Long: `Mark domains as primary bug bounty targets.

Primary domains are highlighted in output and can be filtered with --primary flag.

Examples:
  rep primary api.target.com auth.target.com    Mark as primary
  rep primary --remove api.target.com           Remove from primary
  rep primary --clear                           Clear all primary domains
  rep primary                                   List primary domains`,
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := store.Get()
		if err != nil {
			return fmt.Errorf("failed to load store: %w", err)
		}

		// Clear mode
		if primaryClear {
			domains := s.GetPrimaryDomains()
			count := s.UnsetPrimary(domains...)
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
				pterm.Success.Printf("Cleared primary list (%d domains removed)\n", count)
			}
			return nil
		}

		// No args - list mode
		if len(args) == 0 {
			primary := s.GetPrimaryDomains()
			if getOutputMode() == "json" {
				out, _ := sonic.MarshalIndent(primary, "", "  ")
				fmt.Println(string(out))
			} else {
				if len(primary) == 0 {
					pterm.Info.Println("No primary domains set. Use 'rep primary <domain>' to add.")
				} else {
					pterm.DefaultSection.Println("Primary Domains")
					for _, d := range primary {
						pterm.Success.Printf("  %s\n", d)
					}
					fmt.Printf("\nTotal: %d domains\n", len(primary))
				}
			}
			return nil
		}

		// Remove mode
		if primaryRemove {
			count := s.UnsetPrimary(args...)
			if err := s.Save(); err != nil {
				return fmt.Errorf("failed to save: %w", err)
			}
			if getOutputMode() == "json" {
				out, _ := sonic.MarshalIndent(map[string]interface{}{
					"action":  "remove",
					"domains": args,
					"removed": count,
				}, "", "  ")
				fmt.Println(string(out))
			} else {
				pterm.Success.Printf("Removed %d domain(s) from primary list\n", count)
			}
			return nil
		}

		// Add mode (default)
		count := s.SetPrimary(args...)
		if err := s.Save(); err != nil {
			return fmt.Errorf("failed to save: %w", err)
		}

		if getOutputMode() == "json" {
			out, _ := sonic.MarshalIndent(map[string]interface{}{
				"action":  "add",
				"domains": args,
				"added":   count,
				"total":   len(s.GetPrimaryDomains()),
			}, "", "  ")
			fmt.Println(string(out))
		} else {
			pterm.Success.Printf("Marked %d domain(s) as primary targets\n", count)
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(primaryCmd)
	primaryCmd.Flags().BoolVar(&primaryRemove, "remove", false, "Remove domains from primary list")
	primaryCmd.Flags().BoolVar(&primaryClear, "clear", false, "Clear all primary domains")
}
