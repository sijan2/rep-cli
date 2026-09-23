package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/jevdom"
)

func TestJevActRejectsUnknownPlanBeforeCredentialsAndBrowser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"url":"https://example.test/","steps":[{"id":"bad","action":"eval","javascript":"bad"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	command := newBrowserInteractCommand(jevDOMDependencies{loadConfig: func() (jev.Config, error) { t.Fatal("invalid plan reached credentials"); return jev.Config{}, nil }, browser: func(context.Context, string) (jevdom.Browser, error) {
		t.Fatal("invalid plan reached browser")
		return nil, nil
	}})
	command.SetArgs([]string{"--tab", "12", "--plan", path, "--apply"})
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	if err := command.Execute(); err == nil {
		t.Fatal("unsupported plan accepted")
	}
}
func TestExactInteractionsDoNotLoadModelCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"url":"https://example.test/","steps":[{"id":"name","action":"fill","target":{"name":"Name"},"value":"private-value"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	command := newBrowserInteractCommand(jevDOMDependencies{loadConfig: func() (jev.Config, error) { t.Fatal("exact target loaded model credentials"); return jev.Config{}, nil }, browser: func(context.Context, string) (jevdom.Browser, error) {
		called = true
		return nil, errors.New("fixture browser unavailable")
	}})
	command.SetArgs([]string{"--tab", "12", "--plan", path})
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	if err := command.Execute(); err == nil || !called {
		t.Fatal("exact target did not use the local path")
	}
	if bytes.Contains(output.Bytes(), []byte("private-value")) {
		t.Fatal("input value exposed")
	}
}
