package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bytedance/sonic"
	"github.com/pterm/pterm"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var clearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear all data (live session, saved sessions, ignore list, primary list)",
	Long: `Clear all captured data and reset the store.

This clears:
  - Live session (live.json)
  - All saved sessions in store.json
  - Ignore list
  - Primary domains list

Examples:
  rep clear                Clear everything
  rep clear -o json        JSON output for agents`,
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := store.Get()
		if err != nil {
			return fmt.Errorf("failed to load store: %w", err)
		}

		// Count what we're clearing
		sessionCount := s.SessionCount()
		ignoredCount := len(s.GetIgnoredDomains())
		primaryCount := len(s.GetPrimaryDomains())

		// Get live request count before clearing
		liveCount := 0
		livePath, _ := store.GetLiveFilePath()
		if export, err := loadLiveExport(livePath); err == nil {
			liveCount = len(export.Requests)
		}

		// Clear store completely
		s.ClearAll()

		// Save empty store
		if err := s.Save(); err != nil {
			return fmt.Errorf("failed to save: %w", err)
		}

		// Clear live.json
		clearedLivePath, err := clearLiveExportFile()
		if err != nil {
			pterm.Warning.Printf("Could not clear live.json: %v\n", err)
		}

		if getOutputMode() == "json" {
			result := map[string]interface{}{
				"cleared_live_requests": liveCount,
				"cleared_sessions":      sessionCount,
				"cleared_ignored":       ignoredCount,
				"cleared_primary":       primaryCount,
				"live_path":             clearedLivePath,
			}
			out, _ := sonic.MarshalIndent(result, "", "  ")
			fmt.Println(string(out))
		} else {
			pterm.Success.Println("Cleared all data")
			if liveCount > 0 {
				pterm.Info.Printf("Live requests: %d\n", liveCount)
			}
			if sessionCount > 0 {
				pterm.Info.Printf("Saved sessions: %d\n", sessionCount)
			}
			if ignoredCount > 0 {
				pterm.Info.Printf("Ignored domains: %d\n", ignoredCount)
			}
			if primaryCount > 0 {
				pterm.Info.Printf("Primary domains: %d\n", primaryCount)
			}
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(clearCmd)
}

func clearLiveExportFile() (string, error) {
	livePath, err := store.GetLiveFilePath()
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(livePath), 0755); err != nil {
		return "", err
	}

	export := store.Export{
		Version:    "1.0",
		ExportedAt: time.Now().Format(time.RFC3339),
		Requests:   []store.Request{},
	}

	data, err := sonic.MarshalIndent(export, "", "  ")
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(livePath, data, 0644); err != nil {
		return "", err
	}

	return livePath, nil
}
