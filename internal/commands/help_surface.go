package commands

import "github.com/spf13/cobra"

func configureCommandGroups() {
	if len(rootCmd.Groups()) > 0 {
		return
	}
	rootCmd.AddGroup(
		&cobra.Group{ID: "browser", Title: "Browser work:"},
		&cobra.Group{ID: "traffic", Title: "Captured traffic:"},
		&cobra.Group{ID: "tools", Title: "Specialized tools:"},
		&cobra.Group{ID: "help", Title: "Context and help:"},
	)
	groups := map[string]string{}
	for group, names := range map[string][]string{
		"browser": {"page", "do", "eval", "shot", "reference", "tabs", "close", "browser", "cdp"},
		"traffic": {"net", "body", "fetch", "replay", "download", "media"},
		"help":    {"scope", "doctor", "jev", "help"},
	} {
		for _, name := range names {
			groups[name] = group
		}
	}
	for _, command := range rootCmd.Commands() {
		command.GroupID = groups[command.Name()]
		if command.GroupID == "" {
			command.GroupID = "tools"
		}
	}
	rootCmd.SetHelpCommandGroupID("help")
	rootCmd.CompletionOptions.DisableDefaultCmd = true
}
