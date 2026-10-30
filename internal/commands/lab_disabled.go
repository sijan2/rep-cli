//go:build !replab

package commands

import "github.com/repplus/rep-cli/internal/store"

const labBuild = false

// The core never reads device-wide Android captures as a web-data fallback.
func findLabBodyRequest(string) *store.Request     { return nil }
func findLabRequest(string) map[string]interface{} { return nil }
