package agentstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Journal contains operation kinds and outcomes, never source code or values.
func (store *Store) Journal(task, tab, operation, outcome string) error {
	path := filepath.Join(store.Root, "journal.jsonl")
	if err := os.MkdirAll(store.Root, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return json.NewEncoder(f).Encode(map[string]any{"time": time.Now().UTC().Format(time.RFC3339Nano), "task": task, "tab": tab, "operation": operation, "outcome": outcome})
}
