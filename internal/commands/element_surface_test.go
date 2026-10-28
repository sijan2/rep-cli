package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/jevdom"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

func elementTestRoot(deps jevDOMDependencies) *cobra.Command {
	root := &cobra.Command{Use: "rep", SilenceUsage: true, SilenceErrors: true}
	browser := &cobra.Command{Use: "browser"}
	browser.PersistentFlags().String("browser", "arc", "Browser bridge")
	root.AddCommand(browser)
	browser.AddCommand(newBrowserElementCommand(deps),
		markElementCompatibility(newElementSelectCommand(deps, nil), "rep browser element select"),
		markElementCompatibility(newElementSelectBatchCommand(deps, nil), "rep browser element select-batch"),
		markElementCompatibility(newElementObservationCommand(false, deps, nil), "rep browser element observe"),
		markElementCompatibility(newElementObservationCommand(true, deps, nil), "rep browser element validate"))
	return root
}

func configureElementTestScope(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if _, err := scope.Configure(scope.Options{Workspace: "project", Task: "element-test", WorkspaceSet: true, TaskSet: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scope.Reset)
}

func TestElementCanonicalAndLegacyPreserveScopeAndDefaults(t *testing.T) {
	configureElementTestScope(t)
	goals := filepath.Join(t.TempDir(), "goals.json")
	if err := os.WriteFile(goals, []byte(`{"version":1,"goals":[{"id":"one","goal":"Save"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"select", "select-batch", "observe", "validate"} {
		for _, legacy := range []bool{false, true} {
			for _, before := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/legacy=%v/flags-before=%v", verb, legacy, before), func(t *testing.T) {
					calls := 0
					deps := jevDOMDependencies{
						loadConfig: func() (jev.Config, error) { return jev.Config{Model: "jev-1.13.0"}, nil },
						browser: func(_ context.Context, name string) (jevdom.Browser, error) {
							if name != "headless" {
								t.Fatalf("inherited browser lost: %s", name)
							}
							return decisionBrowserFunc(func(_ context.Context, method string, params, out any) error {
								calls++
								p := params.(map[string]any)
								if method != "browser."+verb || p["tab_id"] != 12 || p["owner"] != "project/element-test" || p["kind"] != "text" || p["origin"] != "https://example.test" || p["frame_id"] != "frame" || p["frame_url"] != "https://example.test/frame" || p["root_backend_dom_node_id"] != int64(42) {
									t.Fatalf("observation scope changed: %s %#v", method, p)
								}
								if verb == "validate" && (p["fingerprint"] != "before" || p["generation"] != "generation") {
									t.Fatal("validation identity lost")
								}
								candidates := make([]any, 70)
								for i := range candidates {
									candidates[i] = map[string]any{"id": i}
								}
								*out.(*map[string]any) = map[string]any{"fingerprint": "full", "candidates": candidates, "coverage": map[string]any{"total_candidates": 70}}
								return nil
							}), nil
						},
						selectBatch: func(_ context.Context, _ jevdom.Browser, _ jev.Config, options []jevdom.Options) ([]jevdom.Result, error) {
							calls++
							if len(options) != 1 {
								t.Fatalf("selection count changed: %d", len(options))
							}
							o := options[0]
							if o.Goal != "Save" || o.TabID != 12 || o.Owner != "project/element-test" || o.Kind != "text" || o.Origin != "https://example.test" || o.FrameID != "frame" || o.FrameURL != "https://example.test/frame" || o.ScopeBackendDOMNodeID != 42 || o.Limit != 240 || o.Confidence != 0.8 || o.NoCache || o.Strategy != "auto" || o.ObservationMode != "auto" {
								t.Fatalf("selection defaults or scope changed: %+v", o)
							}
							return []jevdom.Result{{Status: "no_match", Model: "jev-1.13.0"}}, nil
						},
					}
					command := elementTestRoot(deps)
					args := []string{"browser"}
					if before {
						args = append(args, "--browser", "headless")
					}
					if !legacy {
						args = append(args, "element")
					}
					shared := []string{"--tab", "12", "--kind", "text", "--origin", "https://example.test", "--frame", "frame", "--frame-url", "https://example.test/frame", "--root-node", "42"}
					if before && !legacy {
						args = append(args, shared...)
					}
					args = append(args, verb)
					switch verb {
					case "select":
						args = append(args, "Save")
					case "select-batch":
						args = append(args, goals)
					case "validate":
						args = append(args, "--fingerprint", "before", "--generation", "generation")
					}
					if !before || legacy {
						args = append(args, shared...)
					}
					if !before {
						args = append(args, "--browser", "headless")
					}
					var stdout, stderr bytes.Buffer
					command.SetOut(&stdout)
					command.SetErr(&stderr)
					command.SetArgs(args)
					if err := command.Execute(); err != nil {
						t.Fatalf("execute: %v; %s", err, stderr.String())
					}
					if calls != 1 || !json.Valid(stdout.Bytes()) {
						t.Fatalf("wrong operation count or contaminated JSON: %d %s", calls, stdout.String())
					}
					if legacy != strings.Contains(stderr.String(), "Deprecated:") || (legacy && !strings.Contains(stderr.String(), "rep browser element "+verb)) {
						t.Fatalf("wrong migration notice: %s", stderr.String())
					}
					if verb == "observe" || verb == "validate" {
						var data map[string]any
						if err := json.Unmarshal(stdout.Bytes(), &data); err != nil {
							t.Fatal(err)
						}
						if wrapped, ok := data["data"].(map[string]any); ok {
							data = wrapped
						}
						if len(data["candidates"].([]any)) != 64 || data["fingerprint"] != "full" || data["coverage"].(map[string]any)["output_truncated"] != true {
							t.Fatalf("observation budget or identity changed: %#v", data)
						}
					}
				})
			}
		}
	}
}

func TestElementRejectsInvalidInputBeforeDependencies(t *testing.T) {
	for _, args := range [][]string{
		{"select", "Save"},
		{"select", "Save", "--tab", "12", "--goal", "other"},
		{"select", "Save", "--tab", "12", "--root-node", "42"},
		{"select", "Save", "--tab", "12", "--limit", "481"},
		{"observe", "--tab", "12", "--browser", "unsupported"},
		{"observe", "--tab", "12", "--kind", "invalid"},
		{"observe", "--tab", "12", "--root-node", "42"},
		{"observe", "--tab", "12", "--limit", "4097"},
		{"validate", "--tab", "12"},
		{"validate", "--tab", "12", "--fingerprint", "before"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			command := elementTestRoot(jevDOMDependencies{
				loadConfig: func() (jev.Config, error) { t.Fatal("invalid input reached credentials"); return jev.Config{}, nil },
				browser: func(context.Context, string) (jevdom.Browser, error) {
					t.Fatal("invalid input reached browser")
					return nil, nil
				},
			})
			command.SetOut(&bytes.Buffer{})
			command.SetErr(&bytes.Buffer{})
			command.SetArgs(append([]string{"browser", "element"}, args...))
			if err := command.Execute(); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}

func TestElementFlagsHaveOneCanonicalOwner(t *testing.T) {
	command := elementTestRoot(jevDOMDependencies{})
	element, _, err := command.Find([]string{"browser", "element"})
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range element.Commands() {
		for _, name := range []string{"tab", "kind", "origin", "frame", "frame-url", "root-node", "browser"} {
			if child.LocalNonPersistentFlags().Lookup(name) != nil {
				t.Fatalf("%s redeclared inherited --%s", child.CommandPath(), name)
			}
			if child.InheritedFlags().Lookup(name) == nil {
				t.Fatalf("%s lost inherited --%s", child.CommandPath(), name)
			}
		}
	}
}

func TestElementRuntimeUsesInheritedBrowser(t *testing.T) {
	for _, args := range [][]string{{"--browser", "headless", "runtime"}, {"runtime", "--browser", "headless"}} {
		command := &cobra.Command{Use: "browser"}
		command.PersistentFlags().String("browser", "arc", "Browser bridge")
		command.AddCommand(newInheritedBrowserRuntimeCommand(jevDOMDependencies{browser: func(_ context.Context, name string) (jevdom.Browser, error) {
			if name != "headless" {
				t.Fatalf("inherited browser lost: %q", name)
			}
			return decisionBrowserFunc(func(_ context.Context, method string, params, out any) error {
				if method != "jev.runtime" || params != nil {
					t.Fatal("runtime changed its read-only request")
				}
				*out.(*map[string]any) = map[string]any{"version": 1}
				return nil
			}), nil
		}}))
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		command.SetArgs(args)
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestElementRegisteredBrowserCommandsDoNotShadowSelector(t *testing.T) {
	for _, path := range [][]string{{"runtime"}, {"interact"}, {"step"}, {"select"}, {"select-batch"}, {"observe"}, {"validate"}, {"element", "select"}, {"element", "select-batch"}, {"element", "observe"}, {"element", "validate"}} {
		command, _, err := browserCmd.Find(path)
		if err != nil {
			t.Fatal(err)
		}
		if command.LocalNonPersistentFlags().Lookup("browser") != nil {
			t.Fatalf("%s still shadows --browser", command.CommandPath())
		}
		if command.InheritedFlags().Lookup("browser") != browserCmd.PersistentFlags().Lookup("browser") {
			t.Fatalf("%s does not share its browser parent selector", command.CommandPath())
		}
	}
}
