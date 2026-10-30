package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type commandReference struct {
	File string   `json:"file"`
	Line int      `json:"line"`
	Argv []string `json:"argv"`
	Kind string   `json:"kind"`
}

// Resolve references without executing their Run functions (some examples are
// destructive). Unlike help scraping this sees inherited AND hidden flags.
func resolveReference(args []string) (string, error) {
	cmd := rootCmd
	positional := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			break
		}
		if arg == "|" || arg == ";" || arg == "&&" || arg == "#" {
			break
		}
		if strings.HasPrefix(arg, "--") || strings.HasPrefix(arg, "-") && len(arg) == 2 {
			name := strings.SplitN(strings.TrimLeft(arg, "-"), "=", 2)[0]
			// A metavariable such as --format/--section is a documentation
			// alternative; each literal spelling is checked independently.
			if strings.Contains(name, "/--") {
				name = strings.Split(name, "/--")[0]
			}
			if name == "help" || name == "h" || name == "version" || name == "v" {
				continue
			}
			var flag *pflag.Flag
			for parent := cmd; parent != nil && flag == nil; parent = parent.Parent() {
				sets := []*pflag.FlagSet{parent.PersistentFlags()}
				if parent == cmd {
					sets = append(sets, parent.LocalNonPersistentFlags())
				}
				for _, set := range sets {
					if strings.HasPrefix(arg, "--") {
						flag = set.Lookup(name)
					} else {
						flag = set.ShorthandLookup(name)
					}
					if flag != nil {
						break
					}
				}
			}
			if flag == nil {
				return cmd.CommandPath(), fmt.Errorf("unknown flag %s", arg)
			}
			if flag.NoOptDefVal == "" && !strings.Contains(arg, "=") {
				index++
			}
			continue
		}
		if positional {
			continue
		}
		var child *cobra.Command
		for _, candidate := range cmd.Commands() {
			if arg == candidate.Name() {
				child = candidate
				break
			}
			for _, alias := range candidate.Aliases {
				if arg == alias {
					child = candidate
					break
				}
			}
		}
		if child != nil {
			cmd = child
			continue
		}
		if cmd == rootCmd || cmd.HasSubCommands() && cmd.Run == nil && cmd.RunE == nil {
			return cmd.CommandPath(), fmt.Errorf("unknown command %s", arg)
		}
		positional = true
	}
	return cmd.CommandPath(), nil
}

func referenceFailures(t *testing.T) []string {
	t.Helper()
	data, err := exec.Command("python3", "../../scripts/command_references.py", "../..").Output()
	if err != nil {
		t.Fatal(err)
	}
	var references []commandReference
	if err := json.Unmarshal(data, &references); err != nil {
		t.Fatal(err)
	}
	if len(references) < 100 {
		t.Fatalf("reference scanner unexpectedly found only %d examples", len(references))
	}
	var failures []string
	for _, ref := range references {
		if _, err := resolveReference(ref.Argv); err != nil {
			// Do not key the migration debt by line number: edits elsewhere in
			// a file must not silently bless a changed command or flag.
			failures = append(failures, fmt.Sprintf("%s: %s: %v", ref.File, strings.Join(ref.Argv, " "), err))
		}
	}
	sort.Strings(failures)
	return uniqueStrings(failures)
}

func uniqueStrings(values []string) []string {
	result := []string{}
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func TestDocumentedCommandsResolve(t *testing.T) {
	failures := referenceFailures(t)
	path := filepath.Join("testdata", "command-reference-debt.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var baseline []string
	if err := json.Unmarshal(data, &baseline); err != nil {
		t.Fatal(err)
	}
	// Exact file+argv debt only during the parallel migration. The original
	// freeze hook is gone; no environment variable can bless new failures.
	if len(baseline) > 0 && !time.Now().Before(time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("command-reference migration debt expired; remove it at step 7/9")
	}
	allowed := map[string]bool{}
	for _, value := range baseline {
		allowed[value] = true
	}
	for _, value := range failures {
		if !allowed[value] {
			t.Error(value)
		}
	}
	t.Logf("resolved source contracts; %d exact migration exceptions must be removed by Step 7/9", len(failures))
}

func TestCompanionCommandShapes(t *testing.T) {
	data, err := os.ReadFile("testdata/companion-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Argv   []string `json:"argv"`
		Fields []string `json:"fields"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, item := range cases {
		path, err := resolveReference(item.Argv)
		if err != nil {
			t.Errorf("%v: %v", item.Argv, err)
		}
		paths[path] = true
	}
	if len(cases) != 27 || len(paths) != 18 {
		t.Fatalf("companion baseline: %d shapes / %d paths; want 27/18", len(cases), len(paths))
	}
}

func TestReferenceResolverRejectsUnacceptedFlags(t *testing.T) {
	for _, args := range [][]string{{"browser", "page", "--imaginary"}, {"sync"}, {"body", "h_fixture", "--browser", "headless"}} {
		if _, err := resolveReference(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
