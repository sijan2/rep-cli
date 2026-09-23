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
	saveNote string
)

var saveCmd = &cobra.Command{
	Use:   "save",
	Short: "Save current live session to archive",
	Long: `Save the current live.json session to sessions.jsonl as a named session.

The live session remains intact after saving.
Use 'rep list --saved <id>' to view saved sessions.
Use 'rep sessions' to list all saved sessions.

Examples:
  rep save                    Save with auto-generated ID (timestamp)
  rep save --note "auth flow" Save with descriptive note in ID
  rep save -o json            JSON output for agents`,
	RunE: func(cmd *cobra.Command, args []string) error {
		livePath, err := store.GetLiveFilePath()
		if err != nil {
			return err
		}

		// Check if file exists
		if _, err := os.Stat(livePath); os.IsNotExist(err) {
			return emitLiveUnavailable("save", err)
		}

		// Read file
		data, err := os.ReadFile(livePath)
		if err != nil {
			return fmt.Errorf("failed to read file: %w", err)
		}

		// Parse export
		var export store.Export
		if err := sonic.Unmarshal(data, &export); err != nil {
			return fmt.Errorf("failed to parse JSON: %w", err)
		}

		if len(export.Requests) == 0 {
			pterm.Info.Println("No requests to save (live session is empty)")
			return nil
		}

		// Load store
		s, err := store.Get()
		if err != nil {
			return fmt.Errorf("failed to load store: %w", err)
		}

		// Generate session ID
		sessionID := store.GenerateSessionID(saveNote)

		// Add session
		session, err := s.AddSession(sessionID, saveNote, export.Requests, &export)
		if err != nil {
			return fmt.Errorf("failed to write session log: %w", err)
		}

		// Save store
		if err := s.Save(); err != nil {
			return fmt.Errorf("failed to save store: %w", err)
		}

		if getOutputMode() == "json" {
			result := map[string]interface{}{
				"session_id": session.ID,
				"requests":   len(session.Requests),
				"note":       session.Note,
				"hash_id":    session.HashID,
				"timestamp":  session.Timestamp,
			}
			out, _ := sonic.MarshalIndent(result, "", "  ")
			fmt.Println(string(out))
		} else {
			pterm.Success.Printf("Saved session: %s\n", session.ID)
			pterm.Info.Printf("Requests: %d\n", len(session.Requests))
			if session.HashID != "" {
				pterm.Info.Printf("Hash ID: %s\n", session.HashID)
			}
			if session.Note != "" {
				pterm.Info.Printf("Note: %s\n", session.Note)
			}
			pterm.Info.Println("\nTo view this session:")
			fmt.Printf("  rep list --saved %s\n", session.ID)
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(saveCmd)
	saveCmd.Flags().StringVar(&saveNote, "note", "", "Note to include in session ID")
}
