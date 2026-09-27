package jevdom

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestCacheNamespacesIsolateDecisionsWithinOneBoundedDirectory(t *testing.T) {
	directory := t.TempDir()
	a, b := NewCache(directory), NewCache(directory)
	a.Namespace = "credential-owner-a"
	b.Namespace = "credential-owner-b"
	key := digest("same-observation-and-goal")
	now := time.Now()
	a.store(key, evaluated{Model: "jev-a"}, now)
	if _, ok := b.load(key, now); ok {
		t.Fatal("another namespace consumed a cached decision")
	}
	b.store(key, evaluated{Model: "jev-b"}, now)
	first, ok := a.load(key, now)
	if !ok || first.Model != "jev-a" {
		t.Fatal("second namespace replaced first")
	}
	a.remove(key)
	second, ok := b.load(key, now)
	if !ok || second.Model != "jev-b" {
		t.Fatal("namespace removal deleted another decision")
	}
	for i := 0; i < 140; i++ {
		cache := a
		if i%2 != 0 {
			cache = b
		}
		cache.store(digest(fmt.Sprint(i)), evaluated{Model: "jev-a"}, time.Now())
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != maxCacheEntries {
		t.Fatalf("shared cache not globally bounded: %d entries", len(entries))
	}
}
