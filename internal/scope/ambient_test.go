package scope

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAmbientIdentityDecisionTable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	one := map[string]string{"CLAUDE_CODE_SESSION_ID": "synthetic-parent"}
	nested := map[string]string{"CLAUDE_CODE_SESSION_ID": "synthetic-parent", "CODEX_THREAD_ID": "synthetic-child"}
	ancestor := []Process{{3, 2, "zsh", "started"}, {2, 1, "fixture-worker", "started"}}
	cases := []struct {
		name             string
		opts             Options
		owner            string
		facts            IdentityFacts
		identity, source string
	}{
		{"flag wins", Options{Task: "project/a"}, "project/b", IdentityFacts{Env: one}, "project/a", "flag"},
		{"handle wins", Options{}, "project/b", IdentityFacts{Env: map[string]string{"REP_TASK": "c"}}, "project/b", "handle"},
		{"environment", Options{}, "", IdentityFacts{Env: map[string]string{"REP_TASK": "a"}}, "default/a", "environment"},
		{"removed workspace environment", Options{}, "", IdentityFacts{Env: map[string]string{"REP_TASK": "a", "REP_WORKSPACE": "project"}}, "default/a", "environment"},
		{"qualified", Options{}, "", IdentityFacts{Env: map[string]string{"REP_TASK": "project/a"}}, "project/a", "environment"},
		{"global env", Options{}, "", IdentityFacts{Env: map[string]string{"REP_TASK": "global"}}, "global", "environment"},
		{"terminal", Options{}, "", IdentityFacts{Terminal: true}, "global", "terminal"},
		{"ancestor", Options{}, "", IdentityFacts{Ancestors: ancestor}, "default/process-", "ancestor"},
		{"agent before terminal", Options{}, "", IdentityFacts{Env: one, Terminal: true}, "default/agent-", "agent"},
		{"nested agent", Options{}, "", IdentityFacts{Env: nested}, "default/agent-", "agent"},
	}
	identities := map[string]string{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := ResolveIdentity(c.opts, c.owner, c.facts)
			if err != nil || !strings.HasPrefix(s.Identity(), c.identity) || s.TaskSource != c.source {
				t.Fatalf("got %+v %v", s, err)
			}
			identities[c.name] = s.Identity()
			if s.Scoped && s.DataDir != filepath.Join(root, "rep-cli", "workspaces", s.Workspace, "tasks", s.Task) {
				t.Fatalf("storage moved: %s", s.DataDir)
			}
		})
	}
	if identities["nested agent"] == identities["agent before terminal"] {
		t.Fatal("nested agent clobbered parent")
	}
	first, _ := ResolveIdentity(Options{}, "", IdentityFacts{Env: one})
	second, _ := ResolveIdentity(Options{}, "", IdentityFacts{Env: one})
	if first.Identity() != second.Identity() {
		t.Fatal("same-session siblings must honestly share the derived task")
	}
	if _, err := ResolveIdentity(Options{}, "", IdentityFacts{Ancestors: []Process{{1, 0, "init", "start"}, {12, 1, "sshd", "start"}, {13, 12, "tmux", "start"}}}); err == nil {
		t.Fatal("derived from a shared daemon")
	}
}
func TestInheritedTaskIsNamespaceForNestedAgent(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	env := map[string]string{"REP_TASK": "project/a", "CLAUDE_CODE_SESSION_ID": "parent", "CODEX_THREAD_ID": "child"}
	s, err := ResolveIdentity(Options{}, "", IdentityFacts{Env: env})
	if err != nil || s.TaskSource != "env-namespace" || !strings.HasPrefix(s.Identity(), "project/a-") {
		t.Fatalf("%+v %v", s, err)
	}
	key, _ := sessionKey(env)
	env["REP_TASK_SESSION"] = key
	s, err = ResolveIdentity(Options{}, "", IdentityFacts{Env: env})
	if err != nil || s.Identity() != "project/a" || s.TaskSource != "environment" {
		t.Fatalf("setter binding ignored: %+v %v", s, err)
	}
	env["REP_TASK_SESSION"] = "agent-another-session"
	s, err = ResolveIdentity(Options{}, "", IdentityFacts{Env: env})
	if err != nil || s.TaskSource != "env-namespace" {
		t.Fatalf("inherited binding ignored: %+v %v", s, err)
	}
}
