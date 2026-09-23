package jev

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolatedConfig(t *testing.T) {
	t.Helper()
	for _, name := range []string{"JEV", "JEV_API_KEY", "TYPESAFE_API_KEY", "JEV_ENV_FILE", "JEV_MODEL"} {
		t.Setenv(name, "")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func TestConfigStoresPathWithoutKeyAndWorksOutsideWorkingDirectory(t *testing.T) {
	isolatedConfig(t)
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte("OTHER=irrelevant\nexport JEV=\"local-test-secret\" # comment\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := SetEnvFile(envPath); err != nil {
		t.Fatal(err)
	}
	configPath, _ := ConfigPath()
	data, err := os.ReadFile(configPath)
	if err != nil || strings.Contains(string(data), "local-test-secret") {
		t.Fatal("configuration persisted credentials")
	}
	info, _ := os.Stat(configPath)
	if info.Mode().Perm() != 0600 {
		t.Fatal("configuration is not private")
	}
	config, err := LoadConfig()
	if err != nil || config.APIKey != "local-test-secret" || config.Source != "configured env file" {
		t.Fatalf("cannot resolve configured env path: %v", err)
	}
	encoded, _ := json.Marshal(config)
	if strings.Contains(string(encoded), "local-test-secret") {
		t.Fatal("Config serialized the key")
	}
	t.Setenv("JEV_API_KEY", "second")
	t.Setenv("JEV", "first")
	t.Setenv("JEV_MODEL", "jev-1.13.0")
	config, err = LoadConfig()
	if err != nil || config.APIKey != "first" || config.Model != "jev-1.13.0" {
		t.Fatal("wrong configuration precedence")
	}
}

func TestDotenvIsLiteralAndErrorsAreSecretFree(t *testing.T) {
	isolatedConfig(t)
	for _, assignment := range []string{"JEV='$(touch never-executed)'", "JEV=literal-key # comment", "TYPESAFE_API_KEY=alias-key"} {
		envPath := filepath.Join(t.TempDir(), ".env")
		if err := os.WriteFile(envPath, []byte(assignment), 0600); err != nil {
			t.Fatal(err)
		}
		key, err := keyFromEnvFile(envPath)
		if err != nil || key == "" {
			t.Fatalf("literal env parse failed: %v", err)
		}
	}
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte("JEV=\"private-value"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := keyFromEnvFile(envPath); err == nil || strings.Contains(err.Error(), "private-value") {
		t.Fatal("missing safe parse error")
	}
	t.Setenv("JEV_ENV_FILE", envPath)
	if _, err := LoadConfig(); err == nil {
		t.Fatal("invalid explicit env file silently ignored")
	}
}
