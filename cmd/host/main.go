// Native Messaging Host for rep+ Chrome Extension
// This binary receives HTTP requests from the extension and writes them to disk
// for the CLI to consume in real-time.

package main

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/repplus/rep-cli/internal/store"
)

const (
	LiveFileName    = "live.json"
	MaxLiveRequests = 10000 // Prevent unbounded memory growth
)

var (
	keepOnDisconnect bool // If true, don't clear live.json when extension disconnects
)

// Message from extension
type Message struct {
	Type     string    `json:"type"`
	Requests []Request `json:"requests,omitempty"`
	Request  *Request  `json:"request,omitempty"`
	Action   string    `json:"action,omitempty"` // "add", "clear", "sync"
}

// Request matches extension export format
type Request struct {
	ID               string          `json:"id"`
	OriginalID       string          `json:"original_id,omitempty"`
	Method           string          `json:"method"`
	URL              string          `json:"url"`
	PageURL          string          `json:"page_url,omitempty"`
	ResourceType     string          `json:"resource_type,omitempty"`
	Initiator        string          `json:"initiator,omitempty"`
	Headers          store.HeaderMap `json:"headers,omitempty"`
	Body             string          `json:"body,omitempty"`
	Response         *Response       `json:"response,omitempty"`
	ResponseEncoding string          `json:"response_encoding,omitempty"`
	Timestamp        int64           `json:"timestamp"`
}

type Response struct {
	Status  int             `json:"status"`
	Headers store.HeaderMap `json:"headers,omitempty"`
	Body    string          `json:"body,omitempty"`
}

// LiveData is the file format
type LiveData struct {
	Version    string    `json:"version"`
	ExportedAt string    `json:"exported_at"`
	SessionID  string    `json:"session_id,omitempty"` // Unique per connection
	Requests   []Request `json:"requests"`
}

var (
	mu       sync.Mutex
	liveData *LiveData
	dataPath string
)

func main() {
	// Parse flags (for manual testing)
	flag.BoolVar(&keepOnDisconnect, "keep", false, "Keep live.json data when extension disconnects")
	flag.Parse()

	// Environment variable override (useful since native messaging can't pass args)
	if os.Getenv("REP_KEEP_ON_DISCONNECT") == "1" {
		keepOnDisconnect = true
	}

	// Setup data path
	dataPath = getDataPath()
	ensureDir(filepath.Dir(dataPath))

	// Load existing data
	liveData = loadLiveData()

	// Generate session ID only if starting fresh (preserve on reconnect)
	if liveData.SessionID == "" || len(liveData.Requests) == 0 {
		liveData.SessionID = generateSessionID()
	}

	// Process messages from Chrome
	for {
		msg, err := readMessage()
		if err != nil {
			if err == io.EOF {
				// Extension disconnected - clear live.json unless --keep
				if !keepOnDisconnect {
					clearLiveData()
				}
				break
			}
			continue
		}

		response := handleMessage(msg)
		writeMessage(response)
	}
}

func generateSessionID() string {
	return time.Now().Format("20060102-150405")
}

func clearLiveData() {
	mu.Lock()
	defer mu.Unlock()

	liveData.Requests = []Request{}
	liveData.ExportedAt = time.Now().Format(time.RFC3339)
	liveData.SessionID = ""

	content, err := json.MarshalIndent(liveData, "", "  ")
	if err != nil {
		// Log to stderr (won't interfere with native messaging on stdout)
		os.Stderr.WriteString("Error marshaling live data: " + err.Error() + "\n")
		return
	}

	if err := os.WriteFile(dataPath, content, 0644); err != nil {
		os.Stderr.WriteString("Error writing live.json: " + err.Error() + "\n")
	}
}

func getDataPath() string {
	if override := os.Getenv("REPLIVE_PATH"); override != "" {
		path, err := expandHomePath(override)
		if err == nil {
			return path
		}
	}
	// Check XDG_DATA_HOME
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "rep-cli", LiveFileName)
	}
	// Default ~/.local/share/rep-cli/
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "rep-cli", LiveFileName)
}

func expandHomePath(path string) (string, error) {
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

func ensureDir(dir string) {
	os.MkdirAll(dir, 0755)
}

func loadLiveData() *LiveData {
	data := &LiveData{
		Version:  "1.0",
		Requests: []Request{},
	}

	content, err := os.ReadFile(dataPath)
	if err != nil {
		return data
	}

	if err := json.Unmarshal(content, data); err != nil {
		// Log warning but start fresh to avoid data corruption
		os.Stderr.WriteString("Warning: corrupted live.json, starting fresh: " + err.Error() + "\n")
		return &LiveData{
			Version:  "1.0",
			Requests: []Request{},
		}
	}
	return data
}

func saveLiveData() error {
	mu.Lock()
	defer mu.Unlock()
	return saveLiveDataUnlocked()
}

// saveLiveDataUnlocked saves without acquiring mutex (caller must hold lock)
func saveLiveDataUnlocked() error {
	liveData.ExportedAt = time.Now().Format(time.RFC3339)

	content, err := json.MarshalIndent(liveData, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(dataPath, content, 0644)
}

func handleMessage(msg *Message) map[string]interface{} {
	// Acquire mutex for all data modifications to prevent race conditions
	mu.Lock()
	defer mu.Unlock()

	switch msg.Action {
	case "add":
		if msg.Request != nil {
			// Rotate old requests if we hit the limit (prevent memory leak)
			if len(liveData.Requests) >= MaxLiveRequests {
				// Remove oldest 10% to make room
				removeCount := MaxLiveRequests / 10
				liveData.Requests = liveData.Requests[removeCount:]
			}
			liveData.Requests = append(liveData.Requests, *msg.Request)
			saveLiveDataUnlocked() // Already holding lock
			return map[string]interface{}{
				"success": true,
				"action":  "add",
				"count":   len(liveData.Requests),
			}
		}
	case "sync":
		if msg.Requests != nil {
			// Truncate if incoming sync exceeds limit
			if len(msg.Requests) > MaxLiveRequests {
				msg.Requests = msg.Requests[len(msg.Requests)-MaxLiveRequests:]
			}
			liveData.Requests = msg.Requests
			saveLiveDataUnlocked()
			return map[string]interface{}{
				"success": true,
				"action":  "sync",
				"count":   len(liveData.Requests),
			}
		}
	case "clear":
		liveData.Requests = []Request{}
		saveLiveDataUnlocked()
		return map[string]interface{}{
			"success": true,
			"action":  "clear",
		}
	case "ping":
		return map[string]interface{}{
			"success": true,
			"action":  "pong",
			"path":    dataPath,
			"count":   len(liveData.Requests),
		}
	}

	return map[string]interface{}{
		"success": false,
		"error":   "unknown action",
	}
}

// Native messaging protocol: 4-byte length prefix (little-endian) + JSON
func readMessage() (*Message, error) {
	// Read length (4 bytes, little-endian)
	var length uint32
	if err := binary.Read(os.Stdin, binary.LittleEndian, &length); err != nil {
		return nil, err
	}

	// Read message
	content := make([]byte, length)
	if _, err := io.ReadFull(os.Stdin, content); err != nil {
		return nil, err
	}

	var msg Message
	if err := json.Unmarshal(content, &msg); err != nil {
		return nil, err
	}

	return &msg, nil
}

func writeMessage(msg interface{}) error {
	content, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	// Write length
	length := uint32(len(content))
	if err := binary.Write(os.Stdout, binary.LittleEndian, length); err != nil {
		return err
	}

	// Write message
	_, err = os.Stdout.Write(content)
	return err
}
