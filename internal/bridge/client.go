package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const defaultCallTimeout = 35 * time.Second

type Registry struct {
	PID          int    `json:"pid"`
	ParentPID    int    `json:"parent_pid,omitempty"`
	Browser      string `json:"browser"`
	BrowserLabel string `json:"browser_label,omitempty"`
	Socket       string `json:"socket"`
	ExtensionID  string `json:"extension_id,omitempty"`
	ExtensionVer string `json:"extension_version,omitempty"`
	UserAgent    string `json:"user_agent,omitempty"`
	StartedAt    string `json:"started_at"`
	LastSeen     string `json:"last_seen"`
	RegistryPath string `json:"-"`
}

type RPCRequest struct {
	ID        string      `json:"id"`
	Method    string      `json:"method"`
	Params    interface{} `json:"params,omitempty"`
	TimeoutMS int64       `json:"timeout_ms,omitempty"`
}

type RPCError struct {
	Code    string      `json:"code,omitempty"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

type RPCResponse struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

type Client struct {
	Registry Registry
}

func DataDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv("REP_BRIDGE_DIR")); override != "" {
		return expandHome(override)
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
		return filepath.Join(xdg, "rep-cli", "bridges"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "rep-cli", "bridges"), nil
}

func Discover() ([]Registry, error) {
	dir, err := DataDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	registries := make([]Registry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		var registry Registry
		if json.Unmarshal(data, &registry) != nil || registry.Socket == "" {
			continue
		}
		if !processAlive(registry.PID) {
			removeStaleRegistry(dir, path, registry)
			continue
		}
		if info, statErr := os.Stat(registry.Socket); statErr != nil || info.Mode()&os.ModeSocket == 0 {
			_ = os.Remove(path)
			continue
		}
		registry.RegistryPath = path
		registries = append(registries, registry)
	}
	sort.SliceStable(registries, func(i, j int) bool {
		return registries[i].LastSeen > registries[j].LastSeen
	})
	return registries, nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

func removeStaleRegistry(dir, registryPath string, registry Registry) {
	_ = os.Remove(registryPath)
	expectedSocket := filepath.Join(dir, fmt.Sprintf("bridge-%d.sock", registry.PID))
	if filepath.Clean(registry.Socket) == filepath.Clean(expectedSocket) {
		_ = os.Remove(expectedSocket)
	}
}

func Select(ctx context.Context, browser string) (*Client, error) {
	registries, err := Discover()
	if err != nil {
		return nil, fmt.Errorf("discover browser bridges: %w", err)
	}
	wanted := normalizeBrowser(browser)
	var dialErrors []string
	for _, registry := range registries {
		if wanted != "" && normalizeBrowser(registry.Browser) != wanted {
			continue
		}
		client := &Client{Registry: registry}
		probeCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
		var status json.RawMessage
		err := client.Call(probeCtx, "bridge.ping", nil, &status)
		cancel()
		if err == nil {
			return client, nil
		}
		dialErrors = append(dialErrors, fmt.Sprintf("%s(pid=%d): %v", registry.Browser, registry.PID, err))
	}
	if len(registries) == 0 {
		return nil, fmt.Errorf("no live rep+ browser bridge found")
	}
	if wanted != "" && len(dialErrors) == 0 {
		return nil, fmt.Errorf("no live %s rep+ bridge found", wanted)
	}
	return nil, fmt.Errorf("no responsive rep+ browser bridge: %s", strings.Join(dialErrors, "; "))
}

func (c *Client) Call(ctx context.Context, method string, params interface{}, out interface{}) error {
	if c == nil || c.Registry.Socket == "" {
		return fmt.Errorf("browser bridge has no socket")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultCallTimeout)
		defer cancel()
		deadline, _ = ctx.Deadline()
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", c.Registry.Socket)
	if err != nil {
		return fmt.Errorf("connect %s bridge: %w", c.Registry.Browser, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)

	request := RPCRequest{
		ID:        newRequestID(),
		Method:    method,
		Params:    params,
		TimeoutMS: max(1, time.Until(deadline).Milliseconds()),
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return fmt.Errorf("send bridge request: %w", err)
	}
	var response RPCResponse
	if err := json.NewDecoder(conn).Decode(&response); err != nil {
		return fmt.Errorf("read bridge response: %w", err)
	}
	if response.ID != request.ID {
		return fmt.Errorf("bridge response id mismatch: got %q want %q", response.ID, request.ID)
	}
	if response.Error != nil {
		return response.Error
	}
	if out == nil || len(response.Result) == 0 || string(response.Result) == "null" {
		return nil
	}
	if raw, ok := out.(*json.RawMessage); ok {
		*raw = append((*raw)[:0], response.Result...)
		return nil
	}
	if err := json.Unmarshal(response.Result, out); err != nil {
		return fmt.Errorf("decode %s result: %w", method, err)
	}
	return nil
}

func normalizeBrowser(browser string) string {
	value := strings.ToLower(strings.TrimSpace(browser))
	switch value {
	case "", "any", "auto":
		return ""
	case "the browser company", "arc browser":
		return "arc"
	case "google chrome":
		return "chrome"
	default:
		return value
	}
}

func expandHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

func newRequestID() string {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return "rpc_" + hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("rpc_%d", time.Now().UnixNano())
}
