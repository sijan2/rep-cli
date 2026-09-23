package contextview

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	maxCheckpoints     = 64
	checkpointLifetime = 24 * time.Hour
	maxCheckpointBytes = 2 * 1024 * 1024
)

var (
	cursorPattern = regexp.MustCompile(`^c1_[0-9a-f]{64}$`)
	groupPattern  = regexp.MustCompile(`^g1_[0-9a-f]{32}$`)
	hashPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type checkpoint struct {
	Version int               `json:"version"`
	Scope   string            `json:"scope"`
	Source  string            `json:"source"`
	Groups  map[string]string `json:"groups"`
}

type diskCache struct {
	dir string
	now func() time.Time
}

func openCache(dir string) (diskCache, error) {
	if dir == "" {
		return diskCache{}, errors.New("context requires a scope-specific cursor cache directory")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return diskCache{}, fmt.Errorf("create context cursor directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return diskCache{}, errors.New("context cursor directory must be a regular directory")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return diskCache{}, fmt.Errorf("secure context cursor directory: %w", err)
	}
	return diskCache{dir: dir, now: time.Now}, nil
}

func (cache diskCache) load(cursor, scope, source string) (checkpoint, error) {
	if !cursorPattern.MatchString(cursor) {
		return checkpoint{}, errors.New("invalid context cursor; request a full context without --since")
	}
	path := filepath.Join(cache.dir, cursor+".json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || cache.now().Sub(info.ModTime()) > checkpointLifetime || info.Size() > maxCheckpointBytes {
		return checkpoint{}, errors.New("context cursor is unknown or expired; request a full context without --since")
	}
	file, err := os.Open(path)
	if err != nil {
		return checkpoint{}, errors.New("context cursor cannot be read; request a full context without --since")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxCheckpointBytes+1))
	if err != nil || len(data) > maxCheckpointBytes {
		return checkpoint{}, errors.New("invalid context checkpoint")
	}
	var result checkpoint
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil || result.Version != 1 || result.Groups == nil {
		return checkpoint{}, errors.New("invalid context checkpoint")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return checkpoint{}, errors.New("invalid context checkpoint")
	}
	if result.Scope != scope || result.Source != source {
		return checkpoint{}, errors.New("context cursor belongs to a different scope or source; request a full context without --since")
	}
	for id, hash := range result.Groups {
		if !groupPattern.MatchString(id) || !hashPattern.MatchString(hash) {
			return checkpoint{}, errors.New("invalid context checkpoint")
		}
	}
	canonical, _ := json.Marshal(result)
	if "c1_"+digestString(string(canonical)) != cursor {
		return checkpoint{}, errors.New("context checkpoint content does not match its cursor")
	}
	return result, nil
}

func (cache diskCache) save(value checkpoint) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode context checkpoint: %w", err)
	}
	if len(data) > maxCheckpointBytes {
		return "", errors.New("context checkpoint exceeds its capacity; narrow the selected source")
	}
	cursor := "c1_" + digestString(string(data))
	path := filepath.Join(cache.dir, cursor+".json")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return "", errors.New("context checkpoint path is not a regular file")
		}
		if cache.now().Sub(info.ModTime()) <= checkpointLifetime {
			if _, err := cache.load(cursor, value.Scope, value.Source); err != nil {
				return "", err
			}
			if err := cache.prune(cursor); err != nil {
				return "", err
			}
			return cursor, nil
		}
		if err := os.Remove(path); err != nil {
			return "", fmt.Errorf("replace expired context checkpoint: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect context checkpoint: %w", err)
	}
	temp, err := os.CreateTemp(cache.dir, ".context-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create context checkpoint: %w", err)
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return "", fmt.Errorf("write context checkpoint: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", fmt.Errorf("close context checkpoint: %w", err)
	}
	// Link publishes a fully written file atomically, without overwriting any
	// concurrent writer's immutable checkpoint with the same content address.
	if err := os.Link(temp.Name(), path); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("publish context checkpoint: %w", err)
	}
	if err := cache.prune(cursor); err != nil {
		return "", err
	}
	return cursor, nil
}

func (cache diskCache) prune(keep string) error {
	entries, err := os.ReadDir(cache.dir)
	if err != nil {
		return fmt.Errorf("read context cursor directory: %w", err)
	}
	type entry struct {
		name string
		time time.Time
	}
	var valid []entry
	for _, item := range entries {
		name := item.Name()
		if !strings.HasSuffix(name, ".json") || !cursorPattern.MatchString(strings.TrimSuffix(name, ".json")) {
			continue
		}
		info, err := item.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if name == keep+".json" {
			continue
		}
		if cache.now().Sub(info.ModTime()) > checkpointLifetime {
			_ = os.Remove(filepath.Join(cache.dir, name))
			continue
		}
		valid = append(valid, entry{name: name, time: info.ModTime()})
	}
	sort.Slice(valid, func(i, j int) bool {
		if !valid[i].time.Equal(valid[j].time) {
			return valid[i].time.After(valid[j].time)
		}
		return valid[i].name < valid[j].name
	})
	for i := maxCheckpoints - 1; i < len(valid); i++ {
		if err := os.Remove(filepath.Join(cache.dir, valid[i].name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("prune context checkpoint: %w", err)
		}
	}
	return nil
}
