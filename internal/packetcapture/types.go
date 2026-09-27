// Package packetcapture preserves filtered native packet observations. Packet
// bytes, tentative protocol shapes, and capture completeness are separate claims.
package packetcapture

import (
	"errors"
	"time"
)

var (
	ErrUnsupported = errors.New("native packet capture requires macOS with a cgo-enabled libpcap build")
	ErrPermission  = errors.New("native packet capture permission denied")
)

const (
	DefaultDuration     = 10 * time.Second
	DefaultMaxPackets   = 100000
	DefaultMaxBytes     = 64 << 20
	DefaultSnaplen      = 65535
	DefaultBufferBytes  = 4 << 20
	MaxDuration         = time.Hour
	MaxPackets          = 10000000
	MaxBytes            = 1 << 30
	MaxSnaplen          = 262144
	MaxBufferBytes      = 64 << 20
	MaxInspectPackets   = 1000
	MaxInspectScanBytes = 64 << 20
)

type Options struct {
	Interface   string        `json:"interface"`
	Filter      string        `json:"filter"`
	Output      string        `json:"output"`
	Duration    time.Duration `json:"-"`
	MaxPackets  uint64        `json:"max_packets"`
	MaxBytes    uint64        `json:"max_bytes"`
	Snaplen     int           `json:"snaplen"`
	BufferBytes int           `json:"buffer_bytes"`
	// Ready is called once after activation, filter installation and a flushed
	// pcap header. It must return promptly. Failed setup never invokes it.
	Ready func() `json:"-"`
}

func DefaultOptions() Options {
	return Options{Duration: DefaultDuration, MaxPackets: DefaultMaxPackets, MaxBytes: DefaultMaxBytes, Snaplen: DefaultSnaplen, BufferBytes: DefaultBufferBytes}
}

type Interface struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Loopback    bool   `json:"loopback"`
	Up          bool   `json:"up"`
	Running     bool   `json:"running"`
}

type LinkType struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type Limits struct {
	DurationMS  int64  `json:"duration_ms"`
	MaxPackets  uint64 `json:"max_packets"`
	MaxBytes    uint64 `json:"max_bytes"`
	Snaplen     int    `json:"snaplen"`
	BufferBytes int    `json:"buffer_bytes"`
}

type Statistics struct {
	Available        bool   `json:"available"`
	Received         uint64 `json:"received"`
	Dropped          uint64 `json:"dropped"`
	InterfaceDropped uint64 `json:"interface_dropped"`
	Interpretation   string `json:"interpretation"`
}

type Coverage struct {
	State       string   `json:"state"`
	Scope       string   `json:"scope"`
	Reasons     []string `json:"reasons"`
	Limitations []string `json:"limitations"`
}

type Artifact struct {
	Path         string `json:"path"`
	MetadataPath string `json:"metadata_path"`
	SHA256       string `json:"sha256"`
	Bytes        int64  `json:"bytes"`
	PCAPValid    bool   `json:"pcap_valid"`
}

type Result struct {
	Version              int        `json:"version"`
	Collector            string     `json:"collector"`
	BackendVersion       string     `json:"backend_version,omitempty"`
	Warnings             []string   `json:"warnings,omitempty"`
	Interface            string     `json:"interface"`
	Filter               string     `json:"filter"`
	Promiscuous          bool       `json:"promiscuous"`
	Status               string     `json:"status"`
	StopReason           string     `json:"stop_reason"`
	Error                string     `json:"error,omitempty"`
	ErrorCode            string     `json:"error_code,omitempty"`
	StartedAt            time.Time  `json:"started_at"`
	StoppedAt            time.Time  `json:"stopped_at"`
	ObservationStartedAt *time.Time `json:"observation_started_at,omitempty"`
	DurationMS           int64      `json:"duration_ms"`
	Limits               Limits     `json:"limits"`
	Datalink             LinkType   `json:"datalink"`
	Packets              uint64     `json:"packets"`
	CapturedBytes        uint64     `json:"captured_bytes"`
	OriginalBytes        uint64     `json:"original_bytes"`
	TruncatedPackets     uint64     `json:"truncated_packets"`
	Stats                Statistics `json:"stats"`
	Coverage             Coverage   `json:"coverage"`
	Artifact             Artifact   `json:"artifact"`
}

type InspectOptions struct {
	Offset       int64
	Limit        int
	MaxScanBytes int64
}

type Candidate struct {
	Kind     string `json:"kind"`
	Basis    string `json:"basis"`
	Verified bool   `json:"verified"`
}

type UDPHeader struct {
	SourcePort           uint16 `json:"source_port"`
	DestinationPort      uint16 `json:"destination_port"`
	Length               uint16 `json:"length"`
	CapturedPayloadBytes int64  `json:"captured_payload_bytes"`
}

type Packet struct {
	Offset         int64       `json:"offset"`
	NextOffset     int64       `json:"next_offset"`
	Timestamp      string      `json:"timestamp"`
	CapturedLength uint32      `json:"captured_length"`
	OriginalLength uint32      `json:"original_length"`
	Truncated      bool        `json:"truncated"`
	Network        string      `json:"network,omitempty"`
	SourceIP       string      `json:"source_ip,omitempty"`
	DestinationIP  string      `json:"destination_ip,omitempty"`
	Transport      string      `json:"transport,omitempty"`
	Fragmented     bool        `json:"fragmented,omitempty"`
	UDP            *UDPHeader  `json:"udp,omitempty"`
	Candidates     []Candidate `json:"candidates,omitempty"`
	Notes          []string    `json:"notes,omitempty"`
}

type Inspection struct {
	Path                string    `json:"path"`
	FileBytes           int64     `json:"file_bytes"`
	ModifiedAt          time.Time `json:"modified_at"`
	Format              string    `json:"format"`
	ByteOrder           string    `json:"byte_order"`
	TimestampResolution string    `json:"timestamp_resolution"`
	Datalink            LinkType  `json:"datalink"`
	Snaplen             uint32    `json:"snaplen"`
	Offset              int64     `json:"offset"`
	NextOffset          int64     `json:"next_offset"`
	ScannedBytes        int64     `json:"scanned_bytes"`
	Packets             []Packet  `json:"packets"`
	HasMore             bool      `json:"has_more"`
	PayloadOmitted      bool      `json:"payload_omitted"`
	Interpretation      string    `json:"interpretation"`
}
