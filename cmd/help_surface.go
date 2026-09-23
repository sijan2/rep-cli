package cmd

import "github.com/spf13/cobra"

// Advanced flags remain parseable for scripts and are documented by `describe`.
// This is presentation metadata, not a second configuration or behavior layer.
func advancedFlags(command *cobra.Command, names ...string) {
	for _, name := range names {
		flag := command.Flags().Lookup(name)
		if flag == nil {
			panic("unknown advanced flag: " + name)
		}
		flag.Hidden = true
		if flag.Annotations == nil {
			flag.Annotations = map[string][]string{}
		}
		flag.Annotations["rep/advanced"] = []string{"true"}
	}
}

func configureCommandGroups() {
	if len(rootCmd.Groups()) > 0 {
		return
	}
	rootCmd.AddGroup(
		&cobra.Group{ID: "browser", Title: "Browser automation:"},
		&cobra.Group{ID: "traffic", Title: "Captured traffic:"},
		&cobra.Group{ID: "data", Title: "Workspace and archives:"},
		&cobra.Group{ID: "tools", Title: "Specialized tools:"},
		&cobra.Group{ID: "help", Title: "Help and configuration:"},
	)
	rootGroups := map[string][]string{
		"browser": {"browser", "browse"},
		"traffic": {"body", "chain", "compare", "context", "detail", "diff", "domains", "extract", "findings", "get", "group", "js", "list", "search", "stats", "summary"},
		"data":    {"clear", "ignore", "import", "mute", "note", "primary", "save", "scope", "sessions", "workspace"},
		"help":    {"agent-prompt", "describe", "version", "jev", "completion", "help"},
	}
	groups := map[string]string{}
	for group, names := range rootGroups {
		for _, name := range names {
			groups[name] = group
		}
	}
	for _, command := range rootCmd.Commands() {
		group := groups[command.Name()]
		if group == "" {
			group = "tools"
		}
		command.GroupID = group
	}
	rootCmd.SetHelpCommandGroupID("help")
	rootCmd.SetCompletionCommandGroupID("help")
	browserCmd.AddGroup(
		&cobra.Group{ID: "work", Title: "Everyday workflows:"},
		&cobra.Group{ID: "capture", Title: "Network capture:"},
		&cobra.Group{ID: "raw", Title: "Low-level control:"},
		&cobra.Group{ID: "manage", Title: "Browser management:"},
	)
	browserGroups := map[string]string{"create": "work", "tabs": "work", "close": "work", "interact": "work", "select": "work", "open": "capture", "fetch": "capture", "action": "capture", "watch": "capture", "download": "capture", "eval": "raw", "cdp": "raw", "attach": "raw", "detach": "raw", "targets": "raw", "probe": "raw"}
	for _, command := range browserCmd.Commands() {
		group := browserGroups[command.Name()]
		if group == "" {
			group = "manage"
		}
		command.GroupID = group
	}
}
