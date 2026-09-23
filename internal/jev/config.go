// Package jev integrates TypeSafe's typed Jev decisions. Credentials remain in
// the local CLI/native host and never cross the browser messaging boundary.
package jev

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const Endpoint = "https://api.typesafe.ai/v1/systemone"
const Model = "jev-latest"
const maxEnvBytes = 1024 * 1024

type Config struct {
	APIKey string `json:"-"`
	Source string `json:"source"`
	Model  string `json:"model"`
}

var modelName = regexp.MustCompile(`^jev-[A-Za-z0-9._-]{1,100}$`)

type savedConfig struct {
	EnvFile string `json:"env_file"`
}

func ConfigPath() (string, error) {
	dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("cannot locate Jev configuration directory")
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "rep-cli", "jev.json"), nil
}

// SetEnvFile stores only an absolute source path, never the key itself.
func SetEnvFile(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("invalid Jev env file path")
	}
	if _, err := keyFromEnvFile(absolute); err != nil {
		return "", err
	}
	target, err := ConfigPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return "", errors.New("cannot create Jev configuration directory")
	}
	data, _ := json.MarshalIndent(savedConfig{EnvFile: absolute}, "", "  ")
	f, err := os.CreateTemp(filepath.Dir(target), ".jev-*")
	if err != nil {
		return "", errors.New("cannot write Jev configuration")
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return "", errors.New("cannot write Jev configuration")
	}
	if err := f.Close(); err != nil {
		return "", errors.New("cannot write Jev configuration")
	}
	if err := os.Rename(f.Name(), target); err != nil {
		return "", errors.New("cannot save Jev configuration")
	}
	return absolute, nil
}

func LoadConfig() (Config, error) {
	model := strings.TrimSpace(os.Getenv("JEV_MODEL"))
	if model == "" {
		model = Model
	}
	if !modelName.MatchString(model) {
		return Config{}, errors.New("invalid JEV_MODEL; use a Jev model identifier")
	}
	for _, name := range []string{"JEV", "JEV_API_KEY", "TYPESAFE_API_KEY"} {
		if key := strings.TrimSpace(os.Getenv(name)); key != "" {
			return Config{APIKey: key, Source: "environment:" + name, Model: model}, nil
		}
	}
	path := strings.TrimSpace(os.Getenv("JEV_ENV_FILE"))
	source := "JEV_ENV_FILE"
	if path == "" {
		target, err := ConfigPath()
		if err != nil {
			return Config{}, err
		}
		data, err := os.ReadFile(target)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Config{}, errors.New("cannot read Jev configuration")
		}
		if err == nil {
			var saved savedConfig
			if json.Unmarshal(data, &saved) != nil || !filepath.IsAbs(saved.EnvFile) {
				return Config{}, errors.New("invalid Jev configuration; run rep jev config --env-file PATH")
			}
			path, source = saved.EnvFile, "configured env file"
		}
	}
	if path == "" {
		path, source = ".env", "working-directory .env"
	}
	key, err := keyFromEnvFile(path)
	if err != nil {
		return Config{}, err
	}
	return Config{APIKey: key, Source: source, Model: model}, nil
}

// Parse literal dotenv assignments without executing code or interpolation.
func keyFromEnvFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("Jev key unavailable; set JEV or run rep jev config --env-file PATH")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxEnvBytes+1))
	if err != nil || len(data) > maxEnvBytes {
		return "", errors.New("cannot read Jev env file or file exceeds 1 MiB")
	}
	values := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 4096), maxEnvBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		name, value, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || (name != "JEV" && name != "JEV_API_KEY" && name != "TYPESAFE_API_KEY") {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
			quote := value[0]
			end := strings.IndexByte(value[1:], quote)
			if end < 0 {
				return "", fmt.Errorf("invalid quoted %s assignment in Jev env file", name)
			}
			end++
			tail := strings.TrimSpace(value[end+1:])
			if tail != "" && !strings.HasPrefix(tail, "#") {
				return "", errors.New("invalid Jev env file assignment")
			}
			value = value[1:end]
		} else if at := strings.Index(value, " #"); at >= 0 {
			value = strings.TrimSpace(value[:at])
		}
		values[name] = value
	}
	if scanner.Err() != nil {
		return "", errors.New("cannot parse Jev env file")
	}
	for _, name := range []string{"JEV", "JEV_API_KEY", "TYPESAFE_API_KEY"} {
		if value := strings.TrimSpace(values[name]); value != "" {
			return value, nil
		}
	}
	return "", errors.New("Jev env file has no nonempty JEV, JEV_API_KEY, or TYPESAFE_API_KEY assignment")
}
