package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/spf13/cobra"
)

var androidClearCmd = &cobra.Command{
	Use:   "clear [package]",
	Short: "Clear captured traffic",
	Long: `Clear captured Android traffic data.

Without arguments: clears ALL traffic for ALL packages.
With package: clears traffic only for that package.

Does NOT clear configuration (skip/keep patterns).

Examples:
  rep android clear                              Clear all traffic
  rep android clear com.netflix.mediaclient      Clear only Netflix traffic
  rep android clear --all                        Clear traffic AND config`,
	RunE: func(cmd *cobra.Command, args []string) error {
		home, _ := os.UserHomeDir()
		dataPath := filepath.Join(home, ".local/share/rep-cli/android.json")

		clearConfig, _ := cmd.Flags().GetBool("all")

		if len(args) == 0 {
			// Clear all traffic
			if err := os.Remove(dataPath); err != nil && !os.IsNotExist(err) {
				return err
			}
			fmt.Println("Cleared all Android traffic")

			if clearConfig {
				configPath := filepath.Join(home, ".local/share/rep-cli/android_config.json")
				if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
					return err
				}
				fmt.Println("Cleared all Android config")
			}
			return nil
		}

		// Clear specific package
		pkgQuery := args[0]
		data, err := loadAndroidDataRaw()
		if err != nil {
			fmt.Printf("No traffic to clear\n")
			return nil
		}

		// Find and remove matching package
		found := false
		for name := range data.Packages {
			if strings.Contains(strings.ToLower(name), strings.ToLower(pkgQuery)) {
				delete(data.Packages, name)
				fmt.Printf("Cleared traffic for package %s\n", name)
				found = true
			}
		}

		if !found {
			fmt.Printf("No package matching '%s' found\n", pkgQuery)
			return nil
		}

		// Save updated data
		out, _ := sonic.MarshalIndent(data, "", "  ")
		return os.WriteFile(dataPath, out, 0644)
	},
}

var androidStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show capture status and data summary",
	RunE: func(cmd *cobra.Command, args []string) error {
		home, _ := os.UserHomeDir()

		fmt.Println("┌─────────────────────────────────────────────────────────────┐")
		fmt.Println("│                    Android Capture Status                    │")
		fmt.Println("└─────────────────────────────────────────────────────────────┘")
		fmt.Println()

		// Check if capture is running
		out, _ := exec.Command("pgrep", "-f", "mitmdump").Output()
		if len(out) > 0 {
			fmt.Println("CAPTURE: ✓ Running")
		} else {
			fmt.Println("CAPTURE: ✗ Not running")
			fmt.Println("  Start with: rep android capture")
		}
		fmt.Println()

		// Check data file
		dataPath := filepath.Join(home, ".local/share/rep-cli/android.json")
		if info, err := os.Stat(dataPath); err == nil {
			fmt.Printf("DATA: %.1f KB\n", float64(info.Size())/1024)

			// Load and show summary
			if data, err := loadAndroidDataRaw(); err == nil {
				totalReqs := 0
				for _, p := range data.Packages {
					totalReqs += len(p.Requests)
				}
				fmt.Printf("  Packages: %d\n", len(data.Packages))
				fmt.Printf("  Requests: %d\n", totalReqs)
			}
		} else {
			fmt.Println("DATA: No traffic captured yet")
		}
		fmt.Println()

		// Check config
		if cfg, err := loadAndroidConfig(); err == nil && len(cfg) > 0 {
			fmt.Printf("CONFIG: %d apps configured\n", len(cfg))
			for pkg, c := range cfg {
				skips := len(c.SkipPatterns)
				keeps := len(c.KeepPatterns)
				fmt.Printf("  %s: %d skip, %d keep\n", pkg, skips, keeps)
			}
		} else {
			fmt.Println("CONFIG: No apps configured")
		}
		fmt.Println()

		fmt.Println("COMMANDS:")
		fmt.Println("  rep android capture              Start capture")
		fmt.Println("  rep android capture stop         Stop capture")
		fmt.Println("  rep android summary              View traffic")
		fmt.Println("  rep android config show          View all config")
		fmt.Println("  rep android clear                Clear all traffic")
		fmt.Println("  rep android clear <package>      Clear package traffic")

		return nil
	},
}

func init() {
	androidCmd.AddCommand(androidClearCmd)
	androidCmd.AddCommand(androidStatusCmd)

	androidClearCmd.Flags().Bool("all", false, "Also clear configuration")
}
