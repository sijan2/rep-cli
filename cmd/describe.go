package cmd

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/template"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

// descriptionsFS embeds the tool-contract markdown files so the binary is
// a single file. Descriptions are canonical; --help prose can lag them.
//
//go:embed descriptions/*.md
var descriptionsFS embed.FS

// describeContext carries the values interpolated into each description
// template (budget limits, file paths). Keep this small and stable — agents
// may depend on the field names.
type describeContext struct {
	// Output-layer constants (bytes).
	OverflowThreshold   int
	OverflowPreviewSize int
	MaxBodySize         int
	// Storage paths.
	StorePath  string
	AuthEnvDir string
	SpillDir   string
}

func newDescribeContext() describeContext {
	ctx := describeContext{
		OverflowThreshold:   output.OverflowThreshold,
		OverflowPreviewSize: output.OverflowPreviewSize,
		MaxBodySize:         store.DefaultTruncateConfig().MaxBodySize,
	}
	if p, err := store.GetStoreFilePath(); err == nil {
		ctx.StorePath = p
	}
	if dir, err := getRepConfigDir(); err == nil {
		ctx.AuthEnvDir = dir
	}
	return ctx
}

// knownDescriptions maps `rep describe <name>` → embedded file.
// Adding a new command == add a file + one entry here.
var knownDescriptions = map[string]string{
	"arc":       "descriptions/arc.md",
	"list":      "descriptions/list.md",
	"body":      "descriptions/body.md",
	"browser":   "descriptions/browser.md",
	"headless":  "descriptions/headless.md",
	"curl":      "descriptions/curl.md",
	"download":  "descriptions/download.md",
	"jev":       "descriptions/jev.md",
	"interact":  "descriptions/interact.md",
	"summary":   "descriptions/summary.md",
	"context":   "descriptions/summary.md",
	"scope":     "descriptions/scope.md",
	"workspace": "descriptions/scope.md",
	"setup":     "descriptions/setup.md",
}

var describeCmd = &cobra.Command{
	Use:   "describe <command>",
	Short: "Print the canonical tool contract for a subcommand",
	Long: `Print the machine-parseable contract for a rep subcommand: source, limits,
flags, ID format, empty-result behavior, and failure modes.

Use this to drop a fresh, version-accurate description into an agent's
system prompt. The content lives in cmd/descriptions/ inside the binary
and interpolates live config values (overflow threshold, store path, etc).

Examples:
  rep describe list
  rep describe body
  rep describe list --json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := strings.ToLower(args[0])
		path, ok := knownDescriptions[name]
		if !ok {
			ae := output.NewAgentError(
				output.ErrCodeInvalidArgument,
				"describe",
				fmt.Sprintf("no description for %q (available: %s)", name, strings.Join(describeNames(), ", ")),
				"rep describe list  # one of the known commands",
				"rep agent-prompt   # full paste-ready prompt",
			)
			return output.EmitAgentError(os.Stdout, ae, getOutputMode() == "json")
		}
		rendered, err := renderDescription(path)
		if err != nil {
			return err
		}
		if getOutputMode() == "json" {
			payload := map[string]interface{}{
				"command":     name,
				"description": rendered,
			}
			out, _ := sonic.MarshalIndent(payload, "", "  ")
			fmt.Println(string(out))
			return nil
		}
		fmt.Print(rendered)
		return nil
	},
}

var agentPromptCmd = &cobra.Command{
	Use:   "agent-prompt",
	Short: "Emit a paste-ready system-prompt block for coding agents",
	Long: `Print a markdown block summarizing how an agent should use rep: the
canonical workflow, output contracts, ID format, empty-result shape, and
secret-handling rules. Paste into your agent's system prompt or inject via
your harness's tool-description plumbing.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		rendered, err := renderDescription("descriptions/agent_prompt.md")
		if err != nil {
			return err
		}
		fmt.Print(rendered)
		return nil
	},
}

// renderDescription reads the embedded file and runs it through
// text/template so {{.OverflowThreshold}} etc. get real values.
func renderDescription(path string) (string, error) {
	raw, err := descriptionsFS.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read description %q: %w", path, err)
	}
	tmpl, err := template.New(path).Parse(string(raw))
	if err != nil {
		return "", fmt.Errorf("parse description %q: %w", path, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, newDescribeContext()); err != nil {
		return "", fmt.Errorf("render description %q: %w", path, err)
	}
	return buf.String(), nil
}

func describeNames() []string {
	names := make([]string, 0, len(knownDescriptions))
	for k := range knownDescriptions {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func init() {
	rootCmd.AddCommand(describeCmd)
	rootCmd.AddCommand(agentPromptCmd)
}
