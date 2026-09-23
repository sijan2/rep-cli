package jevdom

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

const CacheTTL = 5 * time.Minute
const maxCacheEntries = 128
const cacheSchema = 2

type Cache struct{ Directory string }

func NewCache(directory string) *Cache { return &Cache{Directory: directory} }

func DefaultCache() *Cache {
	root := os.Getenv("XDG_CACHE_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		root = filepath.Join(home, ".cache")
	}
	return NewCache(filepath.Join(root, "rep-cli", "jev-dom"))
}

type cacheEntry struct {
	Schema    int       `json:"schema"`
	CreatedAt time.Time `json:"created_at"`
	Decision  evaluated `json:"decision"`
}

func cacheKey(fingerprint string, options Options) string {
	// Only this digest is used as the filename. Goal text is never persisted.
	data, _ := json.Marshal([]any{cacheSchema, fingerprint, options.Goal, options.Kind, options.Limit, options.Model})
	return digest(string(data))
}

var cacheName = regexp.MustCompile(`^[0-9a-f]{64}\.json$`)

func (cache *Cache) load(key string, now time.Time) (evaluated, bool) {
	path := filepath.Join(cache.Directory, key+".json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 256*1024 {
		return evaluated{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return evaluated{}, false
	}
	defer f.Close()
	var entry cacheEntry
	if json.NewDecoder(io.LimitReader(f, 256*1024+1)).Decode(&entry) != nil || entry.Schema != cacheSchema || now.Before(entry.CreatedAt) || now.Sub(entry.CreatedAt) >= CacheTTL {
		cache.remove(key)
		return evaluated{}, false
	}
	return entry.Decision, true
}

func (cache *Cache) store(key string, decision evaluated, now time.Time) {
	if cache.Directory == "" || os.MkdirAll(cache.Directory, 0700) != nil {
		return
	}
	if info, err := os.Lstat(cache.Directory); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return
	}
	if os.Chmod(cache.Directory, 0700) != nil {
		return
	}
	data, err := json.Marshal(cacheEntry{Schema: cacheSchema, CreatedAt: now, Decision: decision})
	if err != nil || len(data) > 256*1024 {
		return
	}
	f, err := os.CreateTemp(cache.Directory, ".pending-*")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return
	}
	if f.Close() != nil {
		return
	}
	if os.Rename(f.Name(), filepath.Join(cache.Directory, key+".json")) != nil {
		return
	}
	cache.prune(now)
}

func (cache *Cache) remove(key string) { _ = os.Remove(filepath.Join(cache.Directory, key+".json")) }

func (cache *Cache) prune(now time.Time) {
	entries, err := os.ReadDir(cache.Directory)
	if err != nil {
		return
	}
	type item struct {
		name     string
		modified time.Time
	}
	valid := []item{}
	for _, entry := range entries {
		if !cacheName.MatchString(entry.Name()) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if now.Sub(info.ModTime()) >= CacheTTL {
			_ = os.Remove(filepath.Join(cache.Directory, entry.Name()))
			continue
		}
		valid = append(valid, item{entry.Name(), info.ModTime()})
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].modified.After(valid[j].modified) })
	for _, entry := range valid[min(len(valid), maxCacheEntries):] {
		_ = os.Remove(filepath.Join(cache.Directory, entry.name))
	}
}
