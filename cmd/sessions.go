package cmd

import (
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "List saved sessions",
	Long: `List all saved sessions in store.json.

Use 'rep list --saved <id>' to view a specific session.
Use 'rep save' to save the current live session.

Examples:
  rep sessions              List all sessions
  rep sessions -o json      JSON output for agents`,
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := store.Get()
		if err != nil {
			return fmt.Errorf("failed to load store: %w", err)
		}

		sessions := s.ListSessions()

		if len(sessions) == 0 {
			pterm.Info.Println("No saved sessions")
			pterm.Info.Println("Use 'rep save' to save the current live session")
			return nil
		}

		if getOutputMode() == "json" {
			out := make([]map[string]interface{}, len(sessions))
			for i, sess := range sessions {
				out[i] = map[string]interface{}{
					"id":        sess.ID,
					"requests":  len(sess.Requests),
					"note":      sess.Note,
					"timestamp": sess.Timestamp,
					"time":      time.UnixMilli(sess.Timestamp).Format(time.RFC3339),
				}
			}
			data, _ := sonic.MarshalIndent(out, "", "  ")
			fmt.Println(string(data))
			return nil
		}

		pterm.DefaultSection.Println("Saved Sessions")

		tableData := pterm.TableData{{"ID", "Requests", "Saved At", "Note"}}
		for _, sess := range sessions {
			tableData = append(tableData, []string{
				sess.ID,
				fmt.Sprintf("%d", len(sess.Requests)),
				time.UnixMilli(sess.Timestamp).Format("2006-01-02 15:04:05"),
				sess.Note,
			})
		}

		pterm.DefaultTable.WithHasHeader().WithData(tableData).Render()

		fmt.Println()
		pterm.Info.Println("To view a session: rep list --saved <id>")

		return nil
	},
}

func init() {
	rootCmd.AddCommand(sessionsCmd)
}
