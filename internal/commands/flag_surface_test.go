//go:build !replab

package commands

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Every flag below is part of the final contract. All other accepted spellings
// must have a named, dated migration entry; hiding a flag never avoids counting it.
var contractFlags = map[string][]string{
	"rep": {"task"}, "rep page": {"section", "find", "reload", "viewport", "capture"},
	"rep do": {"press", "file", "capture"}, "rep eval": {"timeout", "capture"},
	"rep shot": {"urls", "viewport", "full-page", "selector"}, "rep reference": {"selector"},
	"rep net": {}, "rep body": {"request", "find", "out"}, "rep fetch": {"request", "header", "data"},
	"rep replay": {"set", "matrix", "write", "timeout"}, "rep download": {"overwrite", "timeout"},
	"rep tabs": {}, "rep close": {}, "rep scope": {}, "rep doctor": {}, "rep browser": {},
	"rep browser start": {"headed"}, "rep browser stop": {}, "rep cdp": {},
}

type surfaceMigration struct {
	Expires  string              `json:"expires"`
	RemoveBy string              `json:"remove_by"`
	Entries  map[string][]string `json:"entries"`
}

func readSurfaceMigration(t *testing.T) surfaceMigration {
	t.Helper()
	data, err := os.ReadFile("testdata/surface-migration.json")
	if err != nil {
		t.Fatal(err)
	}
	var result surfaceMigration
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	expires, err := time.Parse("2006-01-02", result.Expires)
	if err != nil {
		t.Fatal(err)
	}
	if !time.Now().Before(expires) {
		for kind, entries := range result.Entries {
			if len(entries) != 0 {
				t.Errorf("expired %s migration entries: remove at %s (%s)", kind, result.RemoveBy, result.Expires)
			}
		}
	}
	return result
}

func (m surfaceMigration) allows(kind, value string) bool {
	for _, allowed := range m.Entries[kind] {
		if allowed == value {
			return true
		}
	}
	return false
}

func isContractFlag(path, name string) bool {
	for _, expected := range contractFlags[path] {
		if expected == name {
			return true
		}
	}
	return false
}

func surfaceExempt(name string) bool { return name == "help" || name == "version" }

func acceptedFlags(command *cobra.Command) (local, inherited []*pflag.Flag) {
	command.LocalFlags().VisitAll(func(flag *pflag.Flag) {
		if !surfaceExempt(flag.Name) {
			local = append(local, flag)
		}
	})
	command.InheritedFlags().VisitAll(func(flag *pflag.Flag) {
		if !surfaceExempt(flag.Name) {
			inherited = append(inherited, flag)
		}
	})
	return
}

func surfaceDebt() map[string][]string {
	debt := map[string][]string{}
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		path := command.CommandPath()
		if command.Name() == "help" {
			return
		}
		if _, ok := contractFlags[path]; !ok {
			debt["commands"] = append(debt["commands"], path)
		}
		if command.Hidden {
			debt["hidden_commands"] = append(debt["hidden_commands"], path)
		}
		for _, alias := range command.Aliases {
			debt["aliases"] = append(debt["aliases"], path+" => "+alias)
		}
		local, inherited := acceptedFlags(command)
		for _, flag := range local {
			if !isContractFlag(path, flag.Name) {
				debt["flags"] = append(debt["flags"], path+" --"+flag.Name)
			}
		}
		for _, flag := range append(local, inherited...) {
			if flag.Hidden {
				debt["hidden_flags"] = append(debt["hidden_flags"], path+" --"+flag.Name)
			}
		}
		if _, ok := contractFlags[path]; ok && command != rootCmd {
			limit := 600
			if path == "rep do" {
				limit = 1400
			}
			if len(commandContract(command)) > limit {
				debt["help"] = append(debt["help"], path)
			}
		}
		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(rootCmd)
	for path, names := range contractFlags {
		command, args, err := rootCmd.Find(strings.Fields(path)[1:])
		if err != nil || len(args) > 0 || command.CommandPath() != path {
			debt["pending_commands"] = append(debt["pending_commands"], path)
			continue
		}
		for _, name := range names {
			if command.LocalFlags().Lookup(name) == nil {
				debt["pending_flags"] = append(debt["pending_flags"], path+" --"+name)
			}
		}
	}
	files, _ := filepath.Glob("*.go")
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			panic(err)
		}
		for _, decl := range parsed.Decls {
			function, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(function, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				name, ok := call.Fun.(*ast.Ident)
				if ok && name.Name == "advancedFlags" {
					// Exact call text pins its flag arguments, not just its containing file.
					var args []string
					for _, arg := range call.Args {
						switch a := arg.(type) {
						case *ast.Ident:
							args = append(args, a.Name)
						case *ast.BasicLit:
							args = append(args, a.Value)
						default:
							args = append(args, fmt.Sprintf("%T", a))
						}
					}
					debt["advanced_calls"] = append(debt["advanced_calls"], file+":"+function.Name.Name+"("+strings.Join(args, ",")+")")
				}
				return true
			})
		}
	}
	for kind := range debt {
		sort.Strings(debt[kind])
	}
	return debt
}

func TestAcceptedFlagSurface(t *testing.T) {
	migration := readSurfaceMigration(t)
	debt := surfaceDebt()
	for kind, values := range debt {
		for _, value := range values {
			if !migration.allows(kind, value) {
				t.Errorf("unapproved %s: %s; delete compatibility surface, do not hide it", kind, value)
			}
		}
	}
	var localTotal, inheritedTotal, verbs int
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		if command.Name() == "help" {
			return
		}
		path := command.CommandPath()
		local, inherited := acceptedFlags(command)
		localTotal += len(local)
		inheritedTotal += len(inherited)
		if command.Parent() == rootCmd {
			verbs++
		}
		surviving := 0
		for _, flag := range local {
			if !migration.allows("flags", path+" --"+flag.Name) {
				surviving++
			}
		}
		budget := 5
		if command == rootCmd {
			budget = 1
		}
		if surviving > budget {
			t.Errorf("%s accepts %d non-shim local flags; maximum %d", path, surviving, budget)
		}
		for _, flag := range inherited {
			allowed := flag.Name == "task"
			for parent := command.Parent(); parent != nil && !allowed; parent = parent.Parent() {
				if parent.PersistentFlags().Lookup(flag.Name) == flag {
					allowed = migration.allows("flags", parent.CommandPath()+" --"+flag.Name)
				}
			}
			if !allowed {
				t.Errorf("%s inherits unexpected --%s", path, flag.Name)
			}
		}
		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(rootCmd)
	t.Logf("all accepted flags, including hidden: %d local + %d inherited = %d effective; %d top-level verbs", localTotal, inheritedTotal, localTotal+inheritedTotal, verbs)
	if len(debt["commands"]) == 0 && len(debt["flags"]) == 0 && len(debt["pending_commands"]) == 0 && len(debt["pending_flags"]) == 0 {
		if verbs != 16 || localTotal != 29 {
			t.Errorf("final surface: %d verbs, %d local flags including --task; want 16 and 29", verbs, localTotal)
		}
	}
}

func TestContractHelpBudgets(t *testing.T) {
	migration := readSurfaceMigration(t)
	root := rootContract(rootCmd)
	if len(root) > 2000 {
		t.Errorf("root help %d bytes, budget 2000 (~500 tokens)", len(root))
	}
	for path := range contractFlags {
		command, args, err := rootCmd.Find(strings.Fields(path)[1:])
		if err != nil || len(args) > 0 || command.CommandPath() != path || command == rootCmd {
			continue
		}
		help := commandContract(command)
		limit := 600
		if path == "rep do" {
			limit = 1400
		}
		if len(help) > limit && !migration.allows("help", path) {
			t.Errorf("%s help %d bytes exceeds %d (~150 tokens; do schema exception)", path, len(help), limit)
		}
		if strings.Contains(help, "Global Flags:") {
			t.Errorf("%s repeats inherited flags", path)
		}
	}
}

func TestAcceptedFlagAccountingIncludesHiddenAndInherited(t *testing.T) {
	root := &cobra.Command{Use: "fixture"}
	root.PersistentFlags().Bool("shared", false, "")
	child := &cobra.Command{Use: "child"}
	child.Flags().Bool("hidden", false, "")
	child.Flags().Lookup("hidden").Hidden = true
	root.AddCommand(child)
	local, inherited := acceptedFlags(child)
	if len(local) != 1 || len(inherited) != 1 || !local[0].Hidden {
		t.Fatal("accepted inventory omitted hidden or inherited flags")
	}
}
