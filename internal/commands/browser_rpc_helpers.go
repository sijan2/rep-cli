package commands

import (
	"encoding/json"
	"fmt"
	"github.com/repplus/rep-cli/internal/output"

	"os"
	"strings"
)

func readBrowserArgument(value string) (string, error) {
	if !strings.HasPrefix(value, "@") {
		return value, nil
	}
	path := strings.TrimPrefix(value, "@")
	if path == "" {
		return "", fmt.Errorf("missing path after @")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(data), nil
}

func readJSONObject(value string) (map[string]interface{}, error) {
	if strings.TrimSpace(value) == "" {
		return map[string]interface{}{}, nil
	}
	raw, err := readBrowserArgument(value)
	if err != nil {
		return nil, err
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("invalid JSON object: %w", err)
	}
	if result == nil {
		return nil, fmt.Errorf("CDP params must be a JSON object")
	}
	return result, nil
}

func emitBrowserArgumentError(command string, err error) error {
	return output.EmitAgentError(os.Stdout, output.NewAgentError(
		output.ErrCodeInvalidArgument,
		command,
		err.Error(),
		"rep tabs",
		"rep tabs",
	), getOutputMode() == "json")
}

func printBrowserJSON(value interface{}) {
	data, err := output.Marshal(output.Result(value))
	if err == nil {
		fmt.Println(string(data))
	}
}
