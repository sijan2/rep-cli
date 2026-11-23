package commands

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/spf13/cobra"
)

func labHandleFixture(t *testing.T) (*cobra.Command, *int, *agentstate.Store, *agentstate.Tab) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	selected, err := scope.Configure(scope.Options{Workspace: "lab-handle", Task: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scope.Reset)
	index, err := coreStore()
	if err != nil {
		t.Fatal(err)
	}
	tab, err := index.Register(agentstate.Browser{Name: "dia", Profile: "profile-a", Epoch: "epoch-a"}, 17, selected.Identity(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	command := &cobra.Command{Use: "handle-fixture"}
	command.SetContext(context.Background())
	var id int
	bindLabTabFlag(command.Flags(), &id, "Owned tab")
	command.Flags().String("browser", "dia", "Browser")
	labCmd.AddCommand(command)
	t.Cleanup(func() { labCmd.RemoveCommand(command) })
	return command, &id, index, tab
}

func TestLabHandleResolvesPublicTabAndExactProfile(t *testing.T) {
	command, id, _, tab := labHandleFixture(t)
	if err := command.Flags().Set("tab", tab.Handle); err != nil {
		t.Fatal(err)
	}
	if got := invocationHandle(command, nil); got != tab.Handle {
		t.Fatalf("task inference lost handle: %q", got)
	}
	if err := configureLabTab(command); err != nil {
		t.Fatal(err)
	}
	if *id != 17 {
		t.Fatalf("numeric browser ID = %d", *id)
	}
	directory, err := os.MkdirTemp("/tmp", "rep-lab-profile-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	t.Setenv("REP_BRIDGE_DIR", directory)
	t.Setenv("REP_BROWSER", "profile-b")
	bridgeProfileFixture(t, directory, "dia", "profile-a", "epoch-a")
	bridgeProfileFixture(t, directory, "dia", "profile-b", "epoch-b")
	client, err := connectBrowser(command.Context(), "dia")
	if err != nil || client.Registry.ProfileID != tab.Browser.Profile {
		t.Fatalf("lab handle selected wrong profile: %+v, %v", client, err)
	}
}

func TestLabHandleRejectsWrongTaskOrBrowserBeforeConnection(t *testing.T) {
	for _, kind := range []string{"task", "browser"} {
		t.Run(kind, func(t *testing.T) {
			command, id, index, tab := labHandleFixture(t)
			if err := command.Flags().Set("tab", tab.Handle); err != nil {
				t.Fatal(err)
			}
			if kind == "task" {
				if _, err := scope.Configure(scope.Options{Workspace: "lab-handle", Task: "sibling"}); err != nil {
					t.Fatal(err)
				}
			} else if err := command.Flags().Set("browser", "headless"); err != nil {
				t.Fatal(err)
			}
			err := configureLabTab(command)
			var problem *agentstate.Error
			want := "invalid_argument"
			if kind == "task" {
				want = "owned_by_other_task"
			}
			if !errors.As(err, &problem) || problem.Code != want {
				t.Fatalf("unexpected refusal: %v", err)
			}
			if *id != -1 || command.Context().Value(labTabContextKey{}) != nil {
				t.Fatal("refused handle acquired execution context")
			}
			remembered, err := index.Remembered("lab-handle/sibling")
			if err != nil || remembered != nil {
				t.Fatalf("refusal acquired browser holder: %+v, %v", remembered, err)
			}
		})
	}
}

func TestLabNumericTabRemainsSupported(t *testing.T) {
	command, id, _, _ := labHandleFixture(t)
	if err := command.Flags().Set("tab", "42"); err != nil {
		t.Fatal(err)
	}
	if err := configureLabTab(command); err != nil {
		t.Fatal(err)
	}
	if *id != 42 || invocationHandle(command, nil) != "" || command.Context().Value(labTabContextKey{}) != nil {
		t.Fatal("numeric lab tab changed semantics")
	}
}
