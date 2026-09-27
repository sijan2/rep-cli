package browserdiagnostics

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// RTPLimits bound both parsing work and the resulting JSONL artifact. Zero values
// select defaults. MaxBytes applies to JSONL output, including its line endings.
type RTPLimits struct {
	MaxBytes      int64
	MaxInputBytes int64
	MaxPackets    int64
	MaxLineBytes  int
}

func DefaultRTPLimits() RTPLimits {
	return RTPLimits{MaxBytes: 64 << 20, MaxInputBytes: 64 << 20, MaxPackets: 100000, MaxLineBytes: 256 << 10}
}

// RTPResult describes retained diagnostic records. It does not establish that
// Chromium logged every network packet, or that an RTP stream is decodable.
type RTPResult struct {
	Packets               int64  `json:"packets"`
	RTPPackets            int64  `json:"rtp_packets"`
	RTCPPackets           int64  `json:"rtcp_packets"`
	OutgoingPackets       int64  `json:"outgoing_packets"`
	IncomingPackets       int64  `json:"incoming_packets"`
	PacketBytes           int64  `json:"packet_bytes"`
	OutputBytes           int64  `json:"output_bytes"`
	InputBytes            int64  `json:"input_bytes"`
	SourceLines           int64  `json:"source_lines"`
	MalformedRecords      int64  `json:"malformed_records"`
	OverlongLines         int64  `json:"overlong_lines"`
	TimestampedPackets    int64  `json:"timestamped_packets"`
	UnterminatedFinalLine bool   `json:"unterminated_final_line"`
	Truncated             bool   `json:"truncated"`
	StopReason            string `json:"stop_reason"`
	SHA256                string `json:"sha256"`
	PayloadSHA256         string `json:"payload_sha256"`
}

// NativeRTPPacket preserves the exact plaintext datagram at Chromium's native
// SRTP boundary. The inner timestamp is retained as text because some Chromium
// versions render incorrect hours/minutes. NativePrefix has a local date/time
// without a year and is included only when its association is unambiguous.
// Dynamic RTP payload types alone do not identify a codec.
type NativeRTPPacket struct {
	Ordinal      int64       `json:"ordinal"`
	Direction    string      `json:"direction"`
	PacketKind   string      `json:"packet_kind"`
	TimestampRaw string      `json:"timestamp_raw"`
	NativePrefix string      `json:"native_prefix,omitempty"`
	PacketBytes  int         `json:"packet_bytes"`
	PacketSHA256 string      `json:"packet_sha256"`
	Data         []byte      `json:"data_base64"`
	RTP          *RTPHeader  `json:"rtp,omitempty"`
	RTCP         *RTCPHeader `json:"rtcp,omitempty"`
}

type RTPHeader struct {
	PayloadType  uint8  `json:"payload_type"`
	Sequence     uint16 `json:"sequence"`
	Timestamp    uint32 `json:"timestamp"`
	SSRC         uint32 `json:"ssrc"`
	HeaderBytes  int    `json:"header_bytes"`
	PayloadBytes int    `json:"payload_bytes"`
	PaddingBytes int    `json:"padding_bytes"`
	Marker       bool   `json:"marker"`
}

type RTCPHeader struct {
	PacketType      uint8 `json:"packet_type"`
	CompoundPackets int   `json:"compound_packets"`
}

var nativeRTPPrefix = regexp.MustCompile(`^\[[0-9]+:[0-9]+:[0-9]{4}/[0-9]{6}\.[0-9]{6}:VERBOSE1:(?:[^\[\]\r\n]*/)?srtp_session\.cc:[0-9]+\]\s*$`)
var nativeRTPLine = regexp.MustCompile(`^([IO]) ([0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}) 0000 (.*?) # RTP_DUMP$`)

func effectiveRTPLimits(limits RTPLimits) (RTPLimits, error) {
	defaults := DefaultRTPLimits()
	if limits.MaxBytes < 0 || limits.MaxInputBytes < 0 || limits.MaxPackets < 0 || limits.MaxLineBytes < 0 {
		return limits, errors.New("RTP parser limits must be positive")
	}
	if limits.MaxBytes == 0 {
		limits.MaxBytes = defaults.MaxBytes
	}
	if limits.MaxInputBytes == 0 {
		limits.MaxInputBytes = defaults.MaxInputBytes
	}
	if limits.MaxPackets == 0 {
		limits.MaxPackets = defaults.MaxPackets
	}
	if limits.MaxLineBytes == 0 {
		limits.MaxLineBytes = defaults.MaxLineBytes
	}
	if limits.MaxBytes > 1<<40 || limits.MaxInputBytes > 1<<40 || limits.MaxPackets > 10000000 || limits.MaxLineBytes > 4<<20 {
		return limits, errors.New("RTP parser limits exceed supported bounds")
	}
	return limits, nil
}

// ParseRTPLog streams Chromium's native RTP_DUMP records into bounded JSONL.
// It never invents network headers, codec mappings, wall times, or missing bytes.
// Malformed records and oversized lines are counted and skipped; hard byte and
// packet limits stop parsing and mark the result truncated. A non-nil error may
// leave a partial final output line, so callers must keep the failure status.
func ParseRTPLog(ctx context.Context, input io.Reader, output io.Writer, limits RTPLimits) (result RTPResult, err error) {
	limits, err = effectiveRTPLimits(limits)
	if err != nil {
		return result, err
	}
	if input == nil || output == nil {
		return result, errors.New("RTP parser requires input and output")
	}
	artifactHash, datagramHash := sha256.New(), sha256.New()
	defer func() {
		result.SHA256 = hex.EncodeToString(artifactHash.Sum(nil))
		result.PayloadSHA256 = hex.EncodeToString(datagramHash.Sum(nil))
		if err != nil {
			result.Truncated = true
			if result.StopReason == "" {
				result.StopReason = "error"
			}
		}
	}()
	reader := bufio.NewReaderSize(io.LimitReader(input, limits.MaxInputBytes+1), 64<<10)
	prefix, blankLines := "", 0
	for {
		if err = ctx.Err(); err != nil {
			result.StopReason = "canceled"
			return result, err
		}
		line, overlong, final, consumed, readErr := readRTPLogLine(ctx, reader, limits.MaxLineBytes)
		result.InputBytes += consumed
		if result.InputBytes > limits.MaxInputBytes {
			result.Truncated, result.StopReason = true, "max_input_bytes"
			return result, nil
		}
		if consumed == 0 && errors.Is(readErr, io.EOF) {
			result.StopReason = "end_of_log"
			return result, nil
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return result, fmt.Errorf("read native RTP log: %w", readErr)
		}
		result.SourceLines++
		if final {
			result.UnterminatedFinalLine, result.Truncated = true, true
		}
		if overlong {
			result.OverlongLines++
			result.Truncated = true
			prefix = ""
			if final {
				result.StopReason = "unterminated_line"
				return result, nil
			}
			continue
		}
		text := strings.TrimSuffix(string(line), "\r")
		if nativeRTPPrefix.MatchString(text) {
			prefix, blankLines = text, 0
		} else if text == "" && prefix != "" && blankLines < 2 {
			blankLines++
		} else {
			candidate := strings.Contains(text, "RTP_DUMP") || strings.HasPrefix(text, "I ") || strings.HasPrefix(text, "O ")
			if candidate {
				packet, parseErr := parseNativeRTPPacket(text)
				if parseErr != nil {
					result.MalformedRecords++
					result.Truncated = true
				} else {
					if result.Packets >= limits.MaxPackets {
						result.Truncated, result.StopReason = true, "max_packets"
						return result, nil
					}
					packet.Ordinal, packet.NativePrefix = result.Packets+1, prefix
					encoded, marshalErr := json.Marshal(packet)
					if marshalErr != nil {
						return result, marshalErr
					}
					encoded = append(encoded, '\n')
					if int64(len(encoded)) > limits.MaxBytes-result.OutputBytes {
						result.Truncated, result.StopReason = true, "max_output_bytes"
						return result, nil
					}
					n, writeErr := output.Write(encoded)
					if n < 0 || n > len(encoded) {
						return result, errors.New("native RTP output writer returned invalid byte count")
					}
					artifactHash.Write(encoded[:n])
					result.OutputBytes += int64(n)
					if writeErr != nil {
						return result, fmt.Errorf("write native RTP artifact: %w", writeErr)
					}
					if n != len(encoded) {
						return result, io.ErrShortWrite
					}
					datagramHash.Write(packet.Data)
					result.PacketBytes += int64(len(packet.Data))
					result.Packets++
					if packet.PacketKind == "rtp" {
						result.RTPPackets++
					} else {
						result.RTCPPackets++
					}
					if packet.Direction == "outgoing" {
						result.OutgoingPackets++
					} else {
						result.IncomingPackets++
					}
					if prefix != "" {
						result.TimestampedPackets++
					}
				}
			}
			prefix = ""
		}
		if final {
			result.StopReason = "unterminated_line"
			return result, nil
		}
	}
}

func readRTPLogLine(ctx context.Context, reader *bufio.Reader, maxBytes int) (line []byte, overlong, final bool, consumed int64, err error) {
	for {
		if err = ctx.Err(); err != nil {
			return
		}
		var part []byte
		part, err = reader.ReadSlice('\n')
		consumed += int64(len(part))
		ended := len(part) > 0 && part[len(part)-1] == '\n'
		if ended {
			part = part[:len(part)-1]
		}
		if !overlong {
			if len(part) > maxBytes-len(line) {
				overlong = true
				line = nil
			} else {
				line = append(line, part...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		final = errors.Is(err, io.EOF) && consumed > 0
		return
	}
}

func parseNativeRTPPacket(line string) (NativeRTPPacket, error) {
	var packet NativeRTPPacket
	matches := nativeRTPLine.FindStringSubmatch(line)
	if matches == nil {
		return packet, errors.New("invalid RTP_DUMP record")
	}
	timestamp := matches[2]
	if timestamp[:2] > "23" || timestamp[3:5] > "59" || timestamp[6:8] > "59" {
		return packet, errors.New("invalid RTP_DUMP clock")
	}
	// HexByteString emits one space between bytes. Requiring two hexadecimal
	// digits per token rejects partial bytes and accidental offset continuations.
	encoded := strings.TrimSuffix(matches[3], " ")
	if len(encoded) < 11 || len(encoded) > 65535*3-1 || (len(encoded)+1)%3 != 0 {
		return packet, errors.New("invalid native datagram length")
	}
	data := make([]byte, (len(encoded)+1)/3)
	for i := range data {
		if i > 0 && encoded[3*i-1] != ' ' {
			return packet, errors.New("invalid native datagram separator")
		}
		if _, err := hex.Decode(data[i:i+1], []byte(encoded[3*i:3*i+2])); err != nil {
			return packet, errors.New("invalid native datagram hex")
		}
	}
	if data[0]>>6 != 2 {
		return packet, errors.New("invalid RTP version")
	}
	packet.Data, packet.PacketBytes, packet.TimestampRaw = data, len(data), timestamp
	packet.Direction = "incoming"
	if matches[1] == "O" {
		packet.Direction = "outgoing"
	}
	digest := sha256.Sum256(data)
	packet.PacketSHA256 = hex.EncodeToString(digest[:])
	if data[1] >= 192 && data[1] <= 223 {
		packet.PacketKind = "rtcp"
		header := &RTCPHeader{PacketType: data[1]}
		for offset := 0; offset < len(data); {
			if len(data)-offset < 4 || data[offset]>>6 != 2 || data[offset+1] < 192 || data[offset+1] > 223 {
				return NativeRTPPacket{}, errors.New("invalid compound RTCP header")
			}
			length := (int(binary.BigEndian.Uint16(data[offset+2:])) + 1) * 4
			if length > len(data)-offset {
				return NativeRTPPacket{}, errors.New("truncated RTCP packet")
			}
			if data[offset]&0x20 != 0 {
				padding := int(data[offset+length-1])
				if offset+length != len(data) || padding == 0 || padding > length-4 {
					return NativeRTPPacket{}, errors.New("invalid RTCP padding")
				}
			}
			header.CompoundPackets++
			offset += length
		}
		packet.RTCP = header
		return packet, nil
	}
	if len(data) < 12 {
		return NativeRTPPacket{}, errors.New("truncated RTP header")
	}
	headerBytes := 12 + 4*int(data[0]&15)
	if headerBytes > len(data) {
		return NativeRTPPacket{}, errors.New("truncated RTP CSRC list")
	}
	if data[0]&0x10 != 0 {
		if len(data)-headerBytes < 4 {
			return NativeRTPPacket{}, errors.New("truncated RTP extension")
		}
		headerBytes += 4 + 4*int(binary.BigEndian.Uint16(data[headerBytes+2:]))
		if headerBytes > len(data) {
			return NativeRTPPacket{}, errors.New("truncated RTP extension data")
		}
	}
	padding := 0
	if data[0]&0x20 != 0 {
		padding = int(data[len(data)-1])
		if padding == 0 || padding > len(data)-headerBytes {
			return NativeRTPPacket{}, errors.New("invalid RTP padding")
		}
	}
	packet.PacketKind = "rtp"
	packet.RTP = &RTPHeader{PayloadType: data[1] & 127, Sequence: binary.BigEndian.Uint16(data[2:4]), Timestamp: binary.BigEndian.Uint32(data[4:8]), SSRC: binary.BigEndian.Uint32(data[8:12]), HeaderBytes: headerBytes, PayloadBytes: len(data) - headerBytes - padding, PaddingBytes: padding, Marker: data[1]&128 != 0}
	return packet, nil
}
