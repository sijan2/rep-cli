package cmd

import (
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	ignoreRemove bool
	ignoreClear  bool
	ignoreList   bool
)

var ignoreCmd = &cobra.Command{
	Use:   "ignore [domain...]",
	Short: "Manage domain ignore list",
	Long: `Add or remove domains from the ignore list.

Ignored domains are excluded from 'rep list' and 'rep summary' by default.
This helps focus on target domains for bug bounty hunting.

Examples:
  rep ignore google-analytics.com facebook.net     Add domains to ignore
  rep ignore --remove api.example.com              Remove from ignore list
  rep ignore --list                                Show all ignored domains
  rep ignore --clear                               Clear entire ignore list`,
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := store.Get()
		if err != nil {
			return fmt.Errorf("failed to load store: %w", err)
		}

		// List mode
		if ignoreList {
			ignored := s.GetIgnoredDomains()
			if getOutputMode() == "json" {
				out, _ := sonic.MarshalIndent(ignored, "", "  ")
				fmt.Println(string(out))
			} else {
				if len(ignored) == 0 {
					pterm.Info.Println("No ignored domains")
				} else {
					pterm.DefaultSection.Println("Ignored Domains")
					for _, d := range ignored {
						fmt.Printf("  %s\n", d)
					}
					fmt.Printf("\nTotal: %d domains\n", len(ignored))
				}
			}
			return nil
		}

		// Clear mode
		if ignoreClear {
			count := len(s.GetIgnoredDomains())
			s.ClearIgnoreList()
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
				pterm.Success.Printf("Cleared ignore list (%d domains removed)\n", count)
			}
			return nil
		}

		// Need at least one domain
		if len(args) == 0 {
			// Show current list if no args
			ignored := s.GetIgnoredDomains()
			if getOutputMode() == "json" {
				out, _ := sonic.MarshalIndent(ignored, "", "  ")
				fmt.Println(string(out))
			} else {
				if len(ignored) == 0 {
					pterm.Info.Println("No ignored domains. Use 'rep ignore <domain>' to add.")
				} else {
					pterm.DefaultSection.Println("Ignored Domains")
					for _, d := range ignored {
						fmt.Printf("  %s\n", d)
					}
					fmt.Printf("\nTotal: %d domains\n", len(ignored))
					fmt.Println("\nUse --remove to unignore, --clear to clear all")
				}
			}
			return nil
		}

		// Remove mode
		if ignoreRemove {
			count := s.Unignore(args...)
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
				pterm.Success.Printf("Removed %d domain(s) from ignore list\n", count)
			}
			return nil
		}

		// Add mode (default)
		count := s.Ignore(args...)
		if err := s.Save(); err != nil {
			return fmt.Errorf("failed to save: %w", err)
		}

		if getOutputMode() == "json" {
			out, _ := sonic.MarshalIndent(map[string]interface{}{
				"action": "add",
				"domains": args,
				"added":  count,
				"total":  len(s.GetIgnoredDomains()),
			}, "", "  ")
			fmt.Println(string(out))
		} else {
			pterm.Success.Printf("Added %d domain(s) to ignore list\n", count)
			pterm.Info.Printf("Total ignored: %d domains\n", len(s.GetIgnoredDomains()))
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(ignoreCmd)
	ignoreCmd.Flags().BoolVar(&ignoreRemove, "remove", false, "Remove domains from ignore list")
	ignoreCmd.Flags().BoolVar(&ignoreClear, "clear", false, "Clear entire ignore list")
	ignoreCmd.Flags().BoolVar(&ignoreList, "list", false, "List all ignored domains")
}
