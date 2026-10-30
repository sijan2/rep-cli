package commands

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/browserflow"
	"github.com/spf13/cobra"
)

var updateRepSkill = flag.Bool("update-rep-skill", false, "regenerate the version-matched rep skill and its two mirrors from Cobra metadata")

func TestBareRepAndHelpAreTheSameContract(t *testing.T) {
	bare, stderr, code := runEntryPoint(t)
	if code != 0 || stderr != "" {
		t.Fatalf("bare rep failed: %d %q", code, stderr)
	}
	help, stderr, code := runEntryPoint(t, "--help")
	if code != 0 || stderr != "" || bare != help {
		t.Fatalf("bare rep and --help differ: %d %q", code, stderr)
	}
	if !strings.Contains(help, "If next exists, run it") || strings.Contains(help, "Global Flags:") {
		t.Fatalf("missing compact contract: %s", help)
	}
}

func TestContractIsGeneratedFromCommandMetadata(t *testing.T) {
	root := &cobra.Command{Use: "rep"}
	page := &cobra.Command{Use: "page UNIQUE", Short: "Unique metadata"}
	root.AddCommand(page)
	if !strings.Contains(rootContract(root), "page UNIQUE  Unique metadata") {
		t.Fatal("root contract duplicated command metadata")
	}
	page.Flags().String("find", "", "Match `QUERY`")
	page.PersistentFlags().String("unused", "", "")
	root.PersistentFlags().String("task", "", "context")
	help := commandContract(page)
	if !strings.Contains(help, "--find QUERY") || strings.Contains(help, "--task") {
		t.Fatalf("wrong local flag projection: %s", help)
	}
}

func TestDoHelpPlanDecodes(t *testing.T) {
	help := newCoreDoCommand().Long
	start := strings.Index(help, "{\"version\"")
	if start < 0 {
		t.Fatal("do help must contain a concrete plan")
	}
	end := strings.IndexByte(help[start:], '\n')
	if end < 0 {
		t.Fatal("plan example must end on its own line")
	}
	if _, err := browserflow.Decode(strings.NewReader(help[start : start+end])); err != nil {
		t.Fatalf("documented plan is invalid: %v", err)
	}
}

func TestVersionMatchedSkill(t *testing.T) {
	version, err := os.ReadFile("../../VERSION")
	if err != nil {
		t.Fatal(err)
	}
	body := "---\nname: rep\ndescription: Read and interact with browser pages, inspect observed network requests, replay APIs, and save browser-authenticated files using rep. Use for browser-session work and UI verification; source-code tasks without browser state do not need this skill.\nmetadata:\n  version: \"" + strings.TrimSpace(string(version)) + "\"\n---\n\n# rep\n\nRead `rep --help` once per session. Use the version-matched executable and its returned handles. This contract does not authorize actions beyond the user's request.\n\n```text\n" + rootContract(rootCmd) + "```\n"
	for _, name := range []string{"skills/rep/SKILL.md", ".agents/skills/rep/SKILL.md", ".claude/skills/rep/SKILL.md"} {
		path := filepath.Join("../..", name)
		if *updateRepSkill {
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != body {
			t.Errorf("%s differs from VERSION/Cobra contract; regenerate all copies with go test ./internal/commands -run '^TestVersionMatchedSkill$' -args -update-rep-skill", name)
		}
	}
}
