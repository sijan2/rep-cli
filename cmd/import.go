package cmd

import (
	"fmt"
	"os"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var (
	importNote string
)

var importCmd = &cobra.Command{
	Use:   "import <file>",
	Short: "Import traffic from rep+ extension export as a saved session",
	Long: `Import HTTP traffic from rep+ Chrome extension JSON export.

Imports the file as a saved session that can be viewed with 'rep list --saved'.

Example:
  rep import ./rep_export_2024-01-15.json
  rep import ./traffic.json --note "auth flow"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		filePath := args[0]

		// Read file
		data, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("failed to read file: %w", err)
		}

		// Parse export
		var export store.Export
		if err := sonic.Unmarshal(data, &export); err != nil {
			return fmt.Errorf("failed to parse JSON: %w", err)
		}

		if len(export.Requests) == 0 {
			pterm.Warning.Println("No requests found in export file")
			return nil
		}

		// Load store
		s, err := store.Get()
		if err != nil {
			return fmt.Errorf("failed to load store: %w", err)
		}

		// Generate session ID and save as session
		sessionID := store.GenerateSessionID(importNote)
		session := s.AddSession(sessionID, importNote, export.Requests)

		// Save
		if err := s.Save(); err != nil {
			return fmt.Errorf("failed to save store: %w", err)
		}

		// Get domain stats
		tempStore := store.NewTempStore(export.Requests)
		domains := tempStore.GetDomains()

		// Output
		if getOutputMode() == "json" {
			result := map[string]interface{}{
				"session_id":     session.ID,
				"requests":       len(export.Requests),
				"domains":        len(domains),
				"source":         filePath,
				"export_version": export.Version,
			}
			out, _ := sonic.MarshalIndent(result, "", "  ")
			fmt.Println(string(out))
		} else {
			pterm.Success.Printf("Imported %d requests as session: %s\n", len(export.Requests), session.ID)
			pterm.Info.Printf("Unique domains: %d\n", len(domains))

			if len(domains) > 0 {
				fmt.Println()
				pterm.DefaultSection.Println("Top domains:")
				limit := 10
				if len(domains) < limit {
					limit = len(domains)
				}
				for i := 0; i < limit; i++ {
					d := domains[i]
					fmt.Printf("  %-40s %d requests\n", d.Domain, d.RequestCount)
				}
				if len(domains) > 10 {
					fmt.Printf("  ... and %d more domains\n", len(domains)-10)
				}
			}

			fmt.Println()
			pterm.Info.Printf("View with: rep list --saved %s\n", session.ID)
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(importCmd)
	importCmd.Flags().StringVar(&importNote, "note", "", "Add a note to the imported session")
}
