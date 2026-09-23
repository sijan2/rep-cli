package scope

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cleanScopeEnvironment(t *testing.T) string {
	t.Helper()
	Reset()
	t.Cleanup(Reset)
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("REP_WORKSPACE", "")
	t.Setenv("REP_TASK", "")
	t.Chdir(dir)
	return dir
}

func TestScopePrecedenceAndTaskSelection(t *testing.T) {
	dir := cleanScopeEnvironment(t)
	if _, err := InitProject(dir, "bound-project"); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "src", "nested")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	selected, err := Resolve(Options{CWD: nested})
	if err != nil || selected.Workspace != "bound-project" || selected.Task != "default" || selected.Source != "project" {
		t.Fatalf("project selection = %+v, %v", selected, err)
	}
	t.Setenv("REP_WORKSPACE", "from-env")
	t.Setenv("REP_TASK", "agent-a")
	selected, err = Resolve(Options{CWD: nested})
	if err != nil || selected.Workspace != "from-env" || selected.Task != "agent-a" || selected.Source != "environment" {
		t.Fatalf("environment selection = %+v, %v", selected, err)
	}
	selected, err = Resolve(Options{Workspace: "from-flag", Task: "agent-b", WorkspaceSet: true, TaskSet: true, CWD: nested})
	if err != nil || selected.Workspace != "from-flag" || selected.Task != "agent-b" || selected.Source != "flag" {
		t.Fatalf("flag selection = %+v, %v", selected, err)
	}
	want := filepath.Join(dir, "data", "rep-cli", "workspaces", "from-flag", "tasks", "agent-b")
	if selected.DataDir != want || selected.LivePath != filepath.Join(want, "live.json") {
		t.Fatalf("scope paths: %+v", selected)
	}
}

func TestScopeExplicitGlobalIgnoresEnvironmentAndBinding(t *testing.T) {
	dir := cleanScopeEnvironment(t)
	t.Setenv("REP_WORKSPACE", "../invalid")
	t.Setenv("REP_TASK", "task")
	if err := os.MkdirAll(filepath.Join(dir, ".rep"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, BindingName), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	selected, err := Resolve(Options{Global: true})
	if err != nil || selected.Scoped || !selected.ExplicitGlobal || selected.Source != "global" {
		t.Fatalf("explicit global = %+v, %v", selected, err)
	}
	for _, options := range []Options{{Global: true, WorkspaceSet: true}, {Global: true, Task: "a"}, {Global: true, Workspace: "a"}} {
		if _, err := Resolve(options); err == nil {
			t.Fatalf("accepted conflicting options %+v", options)
		}
	}
}

func TestScopeRejectsInvalidNamesAndUnboundTask(t *testing.T) {
	cleanScopeEnvironment(t)
	for _, name := range []string{"../other", ".", "..", "a/b", "a\\b", "", "UPPER", strings.Repeat("x", 65)} {
		if _, err := Resolve(Options{Workspace: name, WorkspaceSet: true}); err == nil {
			t.Errorf("accepted workspace %q", name)
		}
	}
	if _, err := Resolve(Options{Task: "agent"}); err == nil {
		t.Fatal("accepted a task without a workspace")
	}
	if _, err := Resolve(Options{Workspace: "project", TaskSet: true}); err == nil {
		t.Fatal("accepted an explicitly empty task")
	}
}

func TestScopeMalformedNearestBindingFailsClosed(t *testing.T) {
	dir := cleanScopeEnvironment(t)
	if _, err := InitProject(dir, "parent"); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(dir, "child")
	if err := os.MkdirAll(filepath.Join(child, ".rep"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, contents := range []string{`{`, `{"version":2,"workspace":"child"}`, `{"version":1,"workspace":"../escape"}`, `{"version":1,"workspace":"child","task":"oops"}`, `{"version":1,"workspace":"child"} {}`} {
		if err := os.WriteFile(filepath.Join(child, BindingName), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Resolve(Options{CWD: child}); err == nil {
			t.Fatalf("malformed nearest binding fell through to parent: %s", contents)
		}
	}
}

func TestWorkspaceInitIsIdempotentAndDoesNotReplace(t *testing.T) {
	dir := cleanScopeEnvironment(t)
	path, err := InitProject(dir, "project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InitProject(dir, "project"); err != nil {
		t.Fatal(err)
	}
	if _, err := InitProject(dir, "other"); err == nil {
		t.Fatal("replaced a project's binding")
	}
	selected, err := ReadBinding(path)
	if err != nil || selected.Workspace != "project" {
		t.Fatalf("binding changed: %+v %v", selected, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("binding permissions: %v %v", info, err)
	}
}

func TestScopeConfigureIsProcessLocalAndResettable(t *testing.T) {
	cleanScopeEnvironment(t)
	if _, err := Configure(Options{Workspace: "project", Task: "first"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REP_WORKSPACE", "other")
	t.Setenv("REP_TASK", "second")
	selected, err := Current()
	if err != nil || selected.Workspace != "project" || selected.Task != "first" {
		t.Fatalf("configured scope changed: %+v %v", selected, err)
	}
	Reset()
	selected, err = Current()
	if err != nil || selected.Workspace != "other" || selected.Task != "second" {
		t.Fatalf("reset did not re-resolve environment: %+v %v", selected, err)
	}
}
