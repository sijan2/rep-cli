// Package browserdiagnostics supervises explicit native browser diagnostics in
// a new private profile. It never attaches to a user's existing browser.
package browserdiagnostics

import (
	"time"

	"github.com/repplus/rep-cli/internal/packetcapture"
)

const (
	DefaultDuration             = 15 * time.Second
	MaxDuration                 = 10 * time.Minute
	DefaultMaxLogBytes    int64 = 32 << 20
	DefaultMaxKeyLogBytes int64 = 1 << 20
	DefaultMaxPacketBytes int64 = 64 << 20
)

type Options struct {
	Binary, URL, Output, Interface, Filter      string
	Duration                                    time.Duration
	TLSKeys, WebRTCRTP, Headless                bool
	MaxLogBytes, MaxKeyLogBytes, MaxPacketBytes int64
}

func DefaultOptions() Options {
	return Options{URL: "about:blank", Duration: DefaultDuration, MaxLogBytes: DefaultMaxLogBytes, MaxKeyLogBytes: DefaultMaxKeyLogBytes, MaxPacketBytes: DefaultMaxPacketBytes}
}

type Limits struct {
	DurationMS        int64  `json:"duration_ms"`
	MaxLogBytes       int64  `json:"max_log_bytes"`
	MaxKeyLogBytes    int64  `json:"max_key_log_bytes"`
	MaxPacketBytes    int64  `json:"max_packet_bytes"`
	KeyLogEnforcement string `json:"key_log_enforcement"`
}

type Browser struct {
	Binary         string `json:"binary"`
	Version        string `json:"version,omitempty"`
	PID            int    `json:"pid,omitempty"`
	Port           int    `json:"port,omitempty"`
	Headless       bool   `json:"headless"`
	ProfileRemoved bool   `json:"profile_removed"`
}

type Artifact struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type LogStatistics struct {
	ObservedBytes int64 `json:"observed_bytes"`
	RetainedBytes int64 `json:"retained_bytes"`
	DroppedBytes  int64 `json:"dropped_bytes"`
}

type KeyLogStatistics struct {
	State                 string `json:"state"`
	ObservedBytes         int64  `json:"observed_bytes"`
	RetainedBytes         int64  `json:"retained_bytes"`
	DiscardedBytes        int64  `json:"discarded_bytes"`
	ValidLines            int64  `json:"valid_lines"`
	InvalidLines          int64  `json:"invalid_lines"`
	LimitExceeded         bool   `json:"limit_exceeded"`
	UnterminatedFinalLine bool   `json:"unterminated_final_line"`
}

type Result struct {
	Version       int                   `json:"version"`
	Collector     string                `json:"collector"`
	Status        string                `json:"status"`
	StopReason    string                `json:"stop_reason"`
	Error         string                `json:"error,omitempty"`
	Output        string                `json:"output"`
	ManifestPath  string                `json:"manifest_path"`
	ReadyPath     string                `json:"ready_path"`
	StartedAt     time.Time             `json:"started_at"`
	ReadyAt       *time.Time            `json:"ready_at,omitempty"`
	StoppedAt     time.Time             `json:"stopped_at"`
	DurationMS    int64                 `json:"duration_ms"`
	Browser       Browser               `json:"browser"`
	Limits        Limits                `json:"limits"`
	Log           LogStatistics         `json:"log"`
	KeyLog        KeyLogStatistics      `json:"key_log"`
	RTPState      string                `json:"rtp_state"`
	RTP           *RTPResult            `json:"rtp,omitempty"`
	PacketCapture *packetcapture.Result `json:"packet_capture,omitempty"`
	Artifacts     []Artifact            `json:"artifacts"`
	Limitations   []string              `json:"limitations"`
}
