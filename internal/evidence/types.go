// Package evidence stores task-local diagnostic evidence without interpreting or
// executing its contents. Integrity, collection coverage, and verification are
// independent properties throughout the format.
package evidence

import (
	"encoding/json"
	"time"
)

const (
	Version         = 1
	DefaultLimit    = 20
	MaxLimit        = 100
	MaxRecordBytes  = 64 << 10
	MaxPageBytes    = 256 << 10
	MaxReadBytes    = 64 << 10
	StatusPending   = "pending"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusUnknown   = "unknown"
)

type Run struct {
	Version   int               `json:"version"`
	ID        string            `json:"id"`
	Sequence  uint64            `json:"sequence"`
	CreatedAt time.Time         `json:"created_at"`
	Intent    string            `json:"intent"`
	Stop      string            `json:"stop,omitempty"`
	Identity  map[string]string `json:"identity,omitempty"`
}

// Reference identifies existing evidence; it does not assert that the referenced
// source was complete or that timestamp proximity establishes causality.
type Reference struct {
	Kind     string         `json:"kind"`
	ID       string         `json:"id,omitempty"`
	Path     string         `json:"path,omitempty"`
	SHA256   string         `json:"sha256,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type Operation struct {
	Version      int             `json:"version"`
	ID           string          `json:"id"`
	RunID        string          `json:"run_id"`
	ParentID     string          `json:"parent_id,omitempty"`
	Sequence     uint64          `json:"sequence"`
	Kind         string          `json:"kind"`
	Phase        string          `json:"phase"`
	Status       string          `json:"status"`
	RecordedAt   time.Time       `json:"recorded_at"`
	StartedAt    time.Time       `json:"started_at,omitempty"`
	FinishedAt   time.Time       `json:"finished_at,omitempty"`
	Input        json.RawMessage `json:"input,omitempty"`
	Result       json.RawMessage `json:"result,omitempty"`
	Metadata     map[string]any  `json:"metadata,omitempty"`
	ArtifactIDs  []string        `json:"artifact_ids,omitempty"`
	References   []Reference     `json:"references,omitempty"`
	Error        string          `json:"error,omitempty"`
	ModelClaim   string          `json:"model_claim,omitempty"`
	Verification string          `json:"verification"`
}

// Coverage is the collector/importer's declaration. Hashing does not establish
// completeness. Missing declarations are recorded as unknown.
type Coverage struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type ImportSpec struct {
	Path      string         `json:"path,omitempty"`
	Kind      string         `json:"kind,omitempty"`
	Collector string         `json:"collector,omitempty"`
	Build     string         `json:"build,omitempty"`
	Device    string         `json:"device,omitempty"`
	Trial     string         `json:"trial,omitempty"`
	Coverage  Coverage       `json:"coverage"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type Artifact struct {
	Version    int            `json:"version"`
	ID         string         `json:"id"`
	RunID      string         `json:"run_id"`
	Sequence   uint64         `json:"sequence"`
	ImportedAt time.Time      `json:"imported_at"`
	Kind       string         `json:"kind"`
	Collector  string         `json:"collector,omitempty"`
	Build      string         `json:"build,omitempty"`
	Device     string         `json:"device,omitempty"`
	Trial      string         `json:"trial,omitempty"`
	Coverage   Coverage       `json:"declared_coverage"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	SourceName string         `json:"source_name"`
	Path       string         `json:"path"`
	SHA256     string         `json:"sha256"`
	Size       int64          `json:"size"`
}

type Page struct {
	RunID      string      `json:"run_id"`
	Operations []Operation `json:"operations"`
	Next       string      `json:"next,omitempty"`
	HasMore    bool        `json:"has_more"`
	Total      uint64      `json:"total"`
}

type RunPage struct {
	Runs    []Run  `json:"runs"`
	Next    string `json:"next,omitempty"`
	HasMore bool   `json:"has_more"`
	Total   uint64 `json:"total"`
}

type ArtifactPage struct {
	RunID     string     `json:"run_id"`
	Artifacts []Artifact `json:"artifacts"`
	Next      string     `json:"next,omitempty"`
	HasMore   bool       `json:"has_more"`
	Total     uint64     `json:"total"`
}

type ArtifactBytes struct {
	ArtifactID string `json:"artifact_id"`
	SHA256     string `json:"sha256"`
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"next_offset"`
	Size       int64  `json:"size"`
	HasMore    bool   `json:"has_more"`
	// Encoding/json renders bytes as base64, preserving arbitrary formats.
	Data []byte `json:"data_base64"`
}
