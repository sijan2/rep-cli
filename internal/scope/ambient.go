package scope

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// Marker order is part of the task identity contract. Hash values, never retain
// or print session identifiers. Nested agents include every enclosing marker.
var AgentMarkers = []string{"CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "AI_AGENT", "GEMINI_CLI", "AGENT_SESSION_ID", "OPENAI_SESSION_ID"}

type IdentityFacts struct {
	Env       map[string]string
	Terminal  bool
	Ancestors []Process
}
type Process struct {
	PID, Parent   int
	Name, Started string
}

func sessionKey(env map[string]string) (string, int) {
	var parts []string
	for _, name := range AgentMarkers {
		if value := strings.TrimSpace(env[name]); value != "" {
			parts = append(parts, name+"="+value)
		}
	}
	if len(parts) == 0 {
		return "", 0
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return fmt.Sprintf("agent-%x", digest[:10]), len(parts)
}

func SessionKey() string { key, _ := sessionKey(environment()); return key }
func environment() map[string]string {
	env := map[string]string{}
	for _, item := range os.Environ() {
		key, value, _ := strings.Cut(item, "=")
		env[key] = value
	}
	return env
}

func identityFacts() IdentityFacts {
	facts := IdentityFacts{Env: environment()}
	if tty, err := os.OpenFile("/dev/tty", os.O_RDONLY, 0); err == nil {
		facts.Terminal = term.IsTerminal(int(tty.Fd()))
		tty.Close()
	}
	// Read process names and start times only, never command lines or arguments.
	data, err := exec.Command("ps", "-axo", "pid=,ppid=,lstart=,comm=").Output()
	if err != nil {
		return facts
	}
	processes := map[int]Process{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		pid, e1 := strconv.Atoi(fields[0])
		parent, e2 := strconv.Atoi(fields[1])
		if e1 != nil || e2 != nil {
			continue
		}
		processes[pid] = Process{pid, parent, filepath.Base(strings.Join(fields[7:], " ")), strings.Join(fields[2:7], " ")}
	}
	seen := map[int]bool{}
	for pid := os.Getppid(); pid > 1 && !seen[pid]; {
		seen[pid] = true
		p, ok := processes[pid]
		if !ok {
			break
		}
		facts.Ancestors = append(facts.Ancestors, p)
		pid = p.Parent
	}
	return facts
}

func eligibleAncestor(p Process) bool {
	if p.PID <= 1 || p.Started == "" {
		return false
	}
	name := strings.ToLower(strings.TrimPrefix(p.Name, "-"))
	switch name {
	case "sh", "bash", "zsh", "fish", "dash", "csh", "tcsh", "timeout", "env", "xargs", "parallel", "nohup", "nice", "time", "caffeinate", "sudo", "make", "launchd", "systemd", "init", "tmux", "screen", "sshd", "rep-host":
		return false
	}
	return !strings.Contains(name, "daemon") && !strings.Contains(name, "app-server") && !strings.HasPrefix(name, "tmux:") && !strings.HasPrefix(name, "sshd:")
}

// Identity is the public task name. Existing workspace directories retain their
// original on-disk layout; an unqualified name uses the default workspace.
func (s Scope) Identity() string {
	if !s.Scoped {
		return "global"
	}
	return s.Workspace + "/" + s.Task
}

func ResolveInvocation(opts Options, owner string) (Scope, error) {
	return ResolveIdentity(opts, owner, identityFacts())
}

// ResolveIdentity is pure apart from resolving the configured storage root.
// owner is supplied only after validation by the global handle registry.
func ResolveIdentity(opts Options, owner string, facts IdentityFacts) (Scope, error) {
	base, err := GlobalDataDir()
	if err != nil {
		return Scope{}, err
	}
	key, markers := sessionKey(facts.Env)
	selected := Scope{Source: "ambient", DataDir: base, LivePath: filepath.Join(base, "live.json"), SessionKey: key}
	name, source := "", ""
	switch {
	case opts.Global:
		if opts.WorkspaceSet || opts.TaskSet || opts.Workspace != "" || opts.Task != "" {
			return Scope{}, fmt.Errorf("--global cannot be combined with --workspace or --task")
		}
		name, source = "global", "flag"
	case opts.TaskSet || opts.Task != "":
		name, source = strings.TrimSpace(opts.Task), "flag"
		if name == "" {
			return Scope{}, fmt.Errorf("--task must not be empty")
		}
	case owner != "":
		name, source = owner, "handle"
	case strings.TrimSpace(facts.Env["REP_TASK"]) != "":
		name, source = strings.TrimSpace(facts.Env["REP_TASK"]), "environment"
		binding := facts.Env["REP_TASK_SESSION"]
		if key != "" && ((binding != "" && binding != key) || (binding == "" && markers > 1)) {
			// Shorten only the leaf, keeping the old namespace and its storage root.
			namespace, leaf, found := strings.Cut(name, "/")
			if !found {
				namespace, leaf = "", name
			}
			if len(leaf) > 32 {
				leaf = leaf[:32]
			}
			leaf += "-" + strings.TrimPrefix(key, "agent-")
			name = leaf
			if namespace != "" {
				name = namespace + "/" + leaf
			}
			source = "env-namespace"
		}
	case key != "":
		name, source = key, "agent"
	case facts.Terminal:
		name, source = "global", "terminal"
	default:
		for _, p := range facts.Ancestors {
			if eligibleAncestor(p) {
				digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", p.PID, p.Started)))
				name, source = fmt.Sprintf("process-%x", digest[:10]), "ancestor"
				break
			}
		}
		if name == "" {
			return Scope{}, fmt.Errorf("task_required: no agent session, controlling terminal, or eligible ancestor; use a post-verb --task")
		}
	}
	selected.TaskSource = source
	if name == "global" {
		selected.Source = "global"
		selected.Task = "global"
		selected.ExplicitGlobal = true
		return selected, nil
	}
	workspace, task, qualified := strings.Cut(name, "/")
	if !qualified {
		task = name
		workspace = strings.TrimSpace(opts.Workspace)
		if workspace == "" {
			workspace = strings.TrimSpace(facts.Env["REP_WORKSPACE"])
		}
		if workspace == "" {
			workspace = "default"
		}
	}
	if opts.WorkspaceSet && strings.TrimSpace(opts.Workspace) == "" {
		return Scope{}, fmt.Errorf("--workspace must not be empty")
	}
	if qualified && opts.Workspace != "" && opts.Workspace != workspace {
		return Scope{}, fmt.Errorf("qualified task conflicts with --workspace")
	}
	if err := ValidateName(workspace); err != nil {
		return Scope{}, fmt.Errorf("invalid workspace: %w", err)
	}
	if err := ValidateName(task); err != nil {
		return Scope{}, fmt.Errorf("invalid task: %w", err)
	}
	selected.Scoped, selected.Workspace, selected.Task = true, workspace, task
	selected.DataDir = filepath.Join(base, "workspaces", workspace, "tasks", task)
	selected.LivePath = filepath.Join(selected.DataDir, "live.json")
	return selected, nil
}

func ConfigureInvocation(opts Options, owner string) (Scope, error) {
	value, err := ResolveInvocation(opts, owner)
	if err != nil {
		return Scope{}, err
	}
	active.Lock()
	active.value = &value
	active.Unlock()
	return value, nil
}
