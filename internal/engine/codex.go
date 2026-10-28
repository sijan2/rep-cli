package engine

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

type EngineCodex struct {
	binary string
}

func (e *EngineCodex) executable() (string, error) {
	if e.binary != "" {
		return exec.LookPath(e.binary)
	}
	return exec.LookPath("codex-unm")
}

func (e *EngineCodex) Info() EngineInfo {
	_, err := e.executable()
	info := EngineInfo{Name: "codex", Models: []string{"gpt-6-astra", "gpt-6.1-sol", "gpt-6-luna"}, DefaultModel: "gpt-6.1-sol", Capabilities: EngineCapabilities{ConstrainedChoice: true, Freeform: true}, Tasks: []EngineTask{EngineTaskTriage, EngineTaskAdjudicate}, Available: err == nil}
	if err != nil {
		info.Reason = "codex-unm is not installed or is not executable on PATH"
	}
	return info
}

func (e *EngineCodex) Evaluate(ctx context.Context, request EngineRequest) (EngineResponse, error) {
	if request.input == nil {
		return EngineResponse{}, engineError("engine_invalid_input", "engine requests must be prepared by the registry")
	}
	binary, err := e.executable()
	if err != nil {
		return EngineResponse{}, engineError("engine_unavailable", "codex-unm is unavailable")
	}
	state, err := sonic.Marshal(request.input.state())
	if err != nil {
		return EngineResponse{}, engineError("engine_invalid_input", "cannot encode sanitized facts")
	}
	prompt := engineInstructions(request.Task) + "\nTyped facts:\n" + string(state)
	if len(prompt) > request.Limits.MaxInputBytes {
		return EngineResponse{}, engineError("engine_input_limit", "typed prompt exceeds max-input-bytes")
	}
	schema, err := engineOutputSchema(request.Task)
	if err != nil {
		return EngineResponse{}, engineError("engine_invalid_schema", "cannot encode output schema")
	}
	// An empty working directory and disabled inherited configuration keep
	// repository instructions and MCP configuration out of this typed decision.
	dir, err := os.MkdirTemp("", "rep-engine-*")
	if err != nil {
		return EngineResponse{}, engineError("engine_local_io", "cannot create private engine directory")
	}
	defer os.RemoveAll(dir)
	schemaPath := filepath.Join(dir, "output-schema.json")
	if err := os.WriteFile(schemaPath, schema, 0600); err != nil {
		return EngineResponse{}, engineError("engine_local_io", "cannot write engine output schema")
	}
	args := []string{
		"exec", "--ignore-user-config", "--ignore-rules", "--ephemeral", "--skip-git-repo-check",
		"-s", "read-only", "-m", request.Model,
		"-c", "model_reasoning_effort=" + request.Effort, "-c", "service_tier=priority",
		"-c", "approval_policy=never", "-c", "project_doc_max_bytes=0",
		"-c", "features.shell_tool=false", "-c", "features.unified_exec=false",
		"-c", "features.apps=false", "-c", "features.multi_agent=false", "-c", "web_search=disabled",
		"--color", "never", "--output-schema", schemaPath, "-",
	}
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(callCtx, binary, args...)
	command.Dir, command.Env, command.Stdin = dir, engineCodexEnvironment(), strings.NewReader(prompt)
	command.WaitDelay = 250 * time.Millisecond
	engineConfigureProcess(command)
	stdout := &engineBoundedWriter{limit: request.Limits.MaxOutputBytes, cancel: cancel}
	stderr := &engineBoundedWriter{limit: EngineMaxOutputBytes, cancel: cancel}
	command.Stdout, command.Stderr = stdout, stderr
	err = command.Run()
	engineStopProcess(command)
	if stdout.overflow || stderr.overflow {
		return EngineResponse{}, engineError("engine_output_limit", "codex output exceeded its byte limit")
	}
	if err != nil {
		// Provider stderr may contain echoed prompts or credentials. The bounded
		// diagnostic stream is deliberately never returned in a CLI error.
		return EngineResponse{}, engineError("engine_call_failed", "codex did not complete the typed task")
	}
	return EngineResponse{Output: stdout.buffer.Bytes(), Model: request.Model}, nil
}

type engineBoundedWriter struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
	cancel   context.CancelFunc
}

func (w *engineBoundedWriter) Write(data []byte) (int, error) {
	remaining := max(0, w.limit-w.buffer.Len())
	_, _ = w.buffer.Write(data[:min(len(data), remaining)])
	if len(data) > remaining {
		w.overflow = true
		w.cancel()
	}
	return len(data), nil
}

func engineCodexEnvironment() []string {
	var environment []string
	// Codex authenticates through its own config home or OpenAI key; traffic
	// credentials, Jev keys, and arbitrary caller variables need not be inherited.
	for _, key := range []string{"PATH", "HOME", "USER", "LOGNAME", "TMPDIR", "TEMP", "TMP", "SYSTEMROOT", "COMSPEC", "APPDATA", "LOCALAPPDATA", "CODEX_HOME", "OPENAI_API_KEY", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, exists := os.LookupEnv(key); exists {
			environment = append(environment, key+"="+value)
		}
	}
	return environment
}
