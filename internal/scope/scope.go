// Package scope selects a process-local namespace for captured browser data.
// It deliberately leaves browser bridge discovery and browser profiles shared.
package scope

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const BindingName = ".rep/workspace.json"

type Options struct {
	Workspace    string
	Task         string
	WorkspaceSet bool
	TaskSet      bool
	Global       bool
	CWD          string
}

type Scope struct {
	Scoped         bool   `json:"scoped"`
	Workspace      string `json:"workspace,omitempty"`
	Task           string `json:"task,omitempty"`
	TaskSource     string `json:"task_source,omitempty"`
	Source         string `json:"source"`
	BindingPath    string `json:"binding_path,omitempty"`
	DataDir        string `json:"data_dir"`
	LivePath       string `json:"live_path"`
	ExplicitGlobal bool   `json:"explicit_global,omitempty"`
}

type Binding struct {
	Version   int    `json:"version"`
	Workspace string `json:"workspace"`
}

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

var active struct {
	sync.RWMutex
	value *Scope
}

func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("name must contain 1–64 lowercase letters, digits, '.', '_' or '-', beginning with a letter or digit")
	}
	return nil
}

// GlobalDataDir is independent of task selection, as required for bridge discovery.
func GlobalDataDir() (string, error) {
	if base := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); base != "" {
		if !filepath.IsAbs(base) {
			return "", fmt.Errorf("XDG_DATA_HOME must be an absolute path")
		}
		return filepath.Join(base, "rep-cli"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "rep-cli"), nil
}

// Configure fixes selection for this invocation. No global current-workspace file
// is written, so independent agent processes cannot switch one another's scope.
func Configure(opts Options) (Scope, error) {
	value, err := Resolve(opts)
	if err != nil {
		return Scope{}, err
	}
	active.Lock()
	active.value = &value
	active.Unlock()
	return value, nil
}

func Current() (Scope, error) {
	active.RLock()
	value := active.value
	active.RUnlock()
	if value != nil {
		return *value, nil
	}
	return Resolve(Options{})
}

// Reset clears only the process-local selection. It is useful for embedded CLI
// invocations and tests; it never changes a project binding or persisted data.
func Reset() {
	active.Lock()
	active.value = nil
	active.Unlock()
}

// Resolve uses explicit flags, then environment, then the nearest project
// binding. A task never silently selects the legacy global namespace.
func Resolve(opts Options) (Scope, error) {
	base, err := GlobalDataDir()
	if err != nil {
		return Scope{}, err
	}
	result := Scope{Source: "legacy", DataDir: base, LivePath: filepath.Join(base, "live.json")}
	if opts.Global {
		if opts.WorkspaceSet || opts.TaskSet || opts.Workspace != "" || opts.Task != "" {
			return Scope{}, fmt.Errorf("--global cannot be combined with --workspace or --task")
		}
		result.Source, result.ExplicitGlobal = "global", true
		return result, nil
	}

	workspace := strings.TrimSpace(opts.Workspace)
	if opts.WorkspaceSet || opts.Workspace != "" {
		result.Source = "flag"
		if workspace == "" {
			return Scope{}, fmt.Errorf("--workspace must not be empty")
		}
	} else if workspace = strings.TrimSpace(os.Getenv("REP_WORKSPACE")); workspace != "" {
		result.Source = "environment"
	} else {
		binding, path, err := discoverBinding(opts.CWD)
		if err != nil {
			return Scope{}, err
		}
		if path != "" {
			workspace, result.BindingPath, result.Source = binding.Workspace, path, "project"
		}
	}
	task := strings.TrimSpace(opts.Task)
	if opts.TaskSet || opts.Task != "" {
		result.TaskSource = "flag"
		if task == "" {
			return Scope{}, fmt.Errorf("--task must not be empty")
		}
	} else {
		task = strings.TrimSpace(os.Getenv("REP_TASK"))
		if task != "" {
			result.TaskSource = "environment"
		}
	}
	if workspace == "" {
		if task != "" {
			return Scope{}, fmt.Errorf("a task requires --workspace, REP_WORKSPACE, or a project workspace binding")
		}
		return result, nil
	}
	if err := ValidateName(workspace); err != nil {
		return Scope{}, fmt.Errorf("invalid workspace: %w", err)
	}
	if task == "" {
		task = "default"
		result.TaskSource = "default"
	}
	if err := ValidateName(task); err != nil {
		return Scope{}, fmt.Errorf("invalid task: %w", err)
	}
	result.Scoped, result.Workspace, result.Task = true, workspace, task
	result.DataDir = filepath.Join(base, "workspaces", workspace, "tasks", task)
	result.LivePath = filepath.Join(result.DataDir, "live.json")
	return result, nil
}

func discoverBinding(cwd string) (Binding, string, error) {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return Binding{}, "", err
		}
	}
	dir, err := filepath.Abs(cwd)
	if err != nil {
		return Binding{}, "", err
	}
	for {
		path := filepath.Join(dir, BindingName)
		binding, err := ReadBinding(path)
		if err == nil {
			return binding, path, nil
		}
		if !os.IsNotExist(err) {
			return Binding{}, "", fmt.Errorf("read project binding %s: %w", path, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return Binding{}, "", nil
		}
		dir = parent
	}
}

func ReadBinding(path string) (Binding, error) {
	file, err := os.Open(path)
	if err != nil {
		return Binding{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return Binding{}, err
	}
	if len(data) > 4096 {
		return Binding{}, fmt.Errorf("binding exceeds 4096 bytes")
	}
	var binding Binding
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&binding); err != nil {
		return Binding{}, fmt.Errorf("invalid binding JSON")
	}
	if decoder.Decode(new(interface{})) != io.EOF {
		return Binding{}, fmt.Errorf("invalid binding JSON")
	}
	if binding.Version != 1 {
		return Binding{}, fmt.Errorf("unsupported binding version %d", binding.Version)
	}
	if err := ValidateName(binding.Workspace); err != nil {
		return Binding{}, err
	}
	return binding, nil
}

// InitProject creates a project binding without replacing an existing binding.
func InitProject(directory, workspace string) (string, error) {
	if err := ValidateName(workspace); err != nil {
		return "", fmt.Errorf("invalid workspace: %w", err)
	}
	if directory == "" {
		var err error
		directory, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	path := filepath.Join(directory, BindingName)
	if existing, err := ReadBinding(path); err == nil {
		if existing.Workspace == workspace {
			return path, nil
		}
		return "", fmt.Errorf("project is already bound to workspace %q", existing.Workspace)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	data, _ := json.MarshalIndent(Binding{Version: 1, Workspace: workspace}, "", "  ")
	file, err := os.CreateTemp(filepath.Dir(path), ".workspace-*.tmp")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	// Link is atomic and refuses to clobber a concurrently created binding.
	if err := os.Link(file.Name(), path); err != nil {
		return "", fmt.Errorf("create project binding: %w", err)
	}
	return path, nil
}
