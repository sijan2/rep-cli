package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func engineTestScript(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX subprocess fixture")
	}
	path := filepath.Join(t.TempDir(), "codex-unm")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func engineShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func TestEngineCodexSubprocessContract(t *testing.T) {
	dir := t.TempDir()
	argsPath, promptPath, envPath, cwdPath := filepath.Join(dir, "args"), filepath.Join(dir, "prompt"), filepath.Join(dir, "env"), filepath.Join(dir, "cwd")
	binary := engineTestScript(t, "printf '%s\\n' \"$@\" > "+engineShellQuote(argsPath)+"\n/bin/cat > "+engineShellQuote(promptPath)+"\n/usr/bin/env > "+engineShellQuote(envPath)+"\npwd > "+engineShellQuote(cwdPath)+"\nprintf '%s' "+engineShellQuote(engineTriageResponse))
	t.Setenv("JEV_API_KEY", "JEV_SECRET_SENTINEL")
	t.Setenv("RESEARCH_COOKIE", "COOKIE_SECRET_SENTINEL")
	registry := &EngineRegistry{engines: map[string]Engine{"codex": &EngineCodex{binary: binary}}}
	result, err := registry.Run(context.Background(), EngineOptions{Task: EngineTaskTriage, Model: "gpt-6-luna", Effort: "high"}, []byte(engineTriageFixture))
	if err != nil || len(result.Proposals) != 1 || result.Model != "gpt-6-luna" {
		t.Fatalf("subprocess failed: %+v %v", result, err)
	}
	args, _ := os.ReadFile(argsPath)
	for _, required := range []string{"exec\n", "--skip-git-repo-check\n", "-s\nread-only\n", "-m\ngpt-6-luna\n", "model_reasoning_effort=high\n", "service_tier=priority\n", "approval_policy=never\n", "--output-schema\n", "--ignore-user-config\n", "--ephemeral\n", "project_doc_max_bytes=0\n"} {
		if !strings.Contains(string(args), required) {
			t.Fatalf("missing argument %q: %s", required, args)
		}
	}
	if !strings.HasSuffix(string(args), "-\n") || strings.Contains(string(args), "Typed facts") {
		t.Fatalf("prompt must be on stdin: %s", args)
	}
	prompt, _ := os.ReadFile(promptPath)
	if !strings.Contains(string(prompt), "Typed facts:") || strings.Contains(string(prompt), "orders-family") || strings.Contains(string(prompt), "alice") {
		t.Fatalf("prompt did not use sanitized facts: %s", prompt)
	}
	environment, _ := os.ReadFile(envPath)
	if strings.Contains(string(environment), "SECRET_SENTINEL") {
		t.Fatal("unrelated credentials inherited by model subprocess")
	}
	cwd, _ := os.ReadFile(cwdPath)
	if _, err := os.Stat(strings.TrimSpace(string(cwd))); !os.IsNotExist(err) {
		t.Fatalf("temporary model directory not removed: %v", err)
	}
}

func TestEngineCodexFailureBounds(t *testing.T) {
	for _, tc := range []struct {
		name, script, reason string
		timeout              time.Duration
		outputLimit          int
	}{
		{"stdout overflow", "while :; do printf '12345678901234567890'; done", "engine_output_limit", time.Second, 64},
		{"stderr overflow", "while :; do printf '12345678901234567890' >&2; done", "engine_output_limit", time.Second, 1024},
		{"timeout", "/bin/sleep 5 &\nwait", "engine_timeout_or_canceled", 40 * time.Millisecond, 1024},
		{"nonzero", "printf 'PRIVATE_PROVIDER_RESPONSE'\nprintf 'PRIVATE_PROVIDER_ERROR' >&2\nexit 9", "engine_call_failed", time.Second, 1024},
		{"malformed", "printf 'PRIVATE_INVALID_ANSWER'", "engine_invalid_response", time.Second, 1024},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binary := engineTestScript(t, tc.script)
			registry := &EngineRegistry{engines: map[string]Engine{"codex": &EngineCodex{binary: binary}}}
			limits := EngineDefaultLimits()
			limits.Timeout, limits.MaxOutputBytes = tc.timeout, tc.outputLimit
			start := time.Now()
			result, err := registry.Run(context.Background(), EngineOptions{Task: EngineTaskTriage, Limits: limits}, []byte(engineTriageFixture))
			if err != nil || result.Verdict != EngineNeedsMoreEvidence || result.Reason != tc.reason || time.Since(start) > 2*time.Second {
				t.Fatalf("unbounded or unsafe subprocess failure: %+v %v in %s", result, err, time.Since(start))
			}
		})
	}
}

func TestEngineCodexMissingIsLocalAvailability(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	engine := &EngineCodex{}
	info := engine.Info()
	if info.Available || info.Reason == "" || len(info.Models) != 3 || !info.Capabilities.Freeform {
		t.Fatalf("missing executable corrupted listing: %+v", info)
	}
}
