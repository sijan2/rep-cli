package browserdiagnostics

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func nativeDump(direction string, data []byte) string {
	parts := make([]string, len(data))
	for index, value := range data {
		parts[index] = fmt.Sprintf("%02x", value)
	}
	return direction + " 00:00:21.850 0000 " + strings.Join(parts, " ") + " # RTP_DUMP\n"
}

func fixtureRTP() []byte {
	return []byte{0x80, 0xef, 0x12, 0x34, 0, 0, 0, 9, 0x12, 0x34, 0x56, 0x78, 0xf8, 0xaa, 0xbb}
}

func decodeNativePackets(t *testing.T, data []byte) []NativeRTPPacket {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	var packets []NativeRTPPacket
	for {
		var packet NativeRTPPacket
		err := decoder.Decode(&packet)
		if errors.Is(err, io.EOF) {
			return packets
		}
		if err != nil {
			t.Fatal(err)
		}
		packets = append(packets, packet)
	}
}

func TestParseRTPLogPreservesNativeDatagrams(t *testing.T) {
	data := fixtureRTP()
	prefix := "[24265:5810259:0927/110121.850935:VERBOSE1:third_party/webrtc/pc/srtp_session.cc:549] "
	rtcp := []byte{0x80, 201, 0, 1, 0x12, 0x34, 0x56, 0x78, 0x80, 202, 0, 1, 0x12, 0x34, 0x56, 0x78}
	input := "ordinary Chromium message\n" + prefix + "\n\n" + nativeDump("O", data) + nativeDump("I", data) + nativeDump("I", rtcp)
	var output bytes.Buffer
	result, err := ParseRTPLog(context.Background(), strings.NewReader(input), &output, RTPLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Packets != 3 || result.RTPPackets != 2 || result.RTCPPackets != 1 || result.OutgoingPackets != 1 || result.IncomingPackets != 2 || result.TimestampedPackets != 1 || result.Truncated || result.StopReason != "end_of_log" {
		t.Fatalf("unexpected result: %+v", result)
	}
	packets := decodeNativePackets(t, output.Bytes())
	if len(packets) != 3 || !bytes.Equal(packets[0].Data, data) || !bytes.Equal(packets[1].Data, data) || !bytes.Equal(packets[2].Data, rtcp) {
		t.Fatal("native datagrams changed")
	}
	if packets[0].NativePrefix != prefix || packets[1].NativePrefix != "" || packets[0].TimestampRaw != "00:00:21.850" {
		t.Fatal("log timestamp provenance changed")
	}
	if *packets[0].RTP != (RTPHeader{PayloadType: 111, Sequence: 0x1234, Timestamp: 9, SSRC: 0x12345678, HeaderBytes: 12, PayloadBytes: 3, Marker: true}) {
		t.Fatalf("wrong RTP header: %+v", packets[0].RTP)
	}
	if packets[2].RTCP.PacketType != 201 || packets[2].RTCP.CompoundPackets != 2 {
		t.Fatal("RTCP compound framing lost")
	}
	expectedHash := sha256.Sum256(output.Bytes())
	if result.SHA256 != hex.EncodeToString(expectedHash[:]) || result.OutputBytes != int64(output.Len()) || result.InputBytes != int64(len(input)) {
		t.Fatal("artifact identity or input accounting changed")
	}
	expectedPayloadHash := sha256.Sum256(append(append(append([]byte{}, data...), data...), rtcp...))
	if result.PayloadSHA256 != hex.EncodeToString(expectedPayloadHash[:]) || result.PacketBytes != int64(2*len(data)+len(rtcp)) {
		t.Fatal("native datagram identity changed")
	}
	for i, packet := range packets {
		digest := sha256.Sum256(packet.Data)
		if packet.Ordinal != int64(i+1) || packet.PacketBytes != len(packet.Data) || packet.PacketSHA256 != hex.EncodeToString(digest[:]) {
			t.Fatal("packet identity changed")
		}
	}
}

func TestRTPHeaderExtensionsAndPadding(t *testing.T) {
	data := []byte{0xb1, 96, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3, 0, 0, 0, 4, 0xbe, 0xde, 0, 1, 0x11, 0x23, 0, 0, 0xaa, 0xbb, 0, 2}
	packet, err := parseNativeRTPPacket(strings.TrimSpace(nativeDump("I", data)))
	if err != nil {
		t.Fatal(err)
	}
	if packet.RTP.HeaderBytes != 24 || packet.RTP.PayloadBytes != 2 || packet.RTP.PaddingBytes != 2 || !bytes.Equal(packet.Data, data) {
		t.Fatalf("wrong extension/padding framing: %+v", packet)
	}
}

func TestMalformedRTPRecordsDoNotBecomeEvidence(t *testing.T) {
	cases := [][]byte{
		{0x40, 96, 0, 1},                                           // Wrong version.
		{0x80, 96, 0, 1},                                           // Missing fixed header.
		{0x81, 96, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3},                   // Missing CSRC.
		{0x90, 96, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3},                   // Missing extension header.
		{0x90, 96, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3, 0xbe, 0xde, 0, 1}, // Missing extension bytes.
		{0xa0, 96, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3, 0},                // Zero padding.
		{0xa0, 96, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3, 2},                // Padding exceeds payload.
		{0x80, 201, 0, 2, 0, 0, 0, 1},                              // RTCP declared length exceeds bytes.
		{0x80, 201, 0, 1, 0, 0, 0, 1, 0},                           // Incomplete compound RTCP.
		{0xa0, 201, 0, 1, 0, 0, 0, 0},                              // Zero RTCP padding.
		{0xa0, 201, 0, 1, 0, 0, 0, 5},                              // RTCP padding exceeds packet.
		{0xa0, 201, 0, 1, 0, 0, 0, 4, 0x80, 201, 0, 1, 0, 0, 0, 1}, // Padding on non-final RTCP.
	}
	var input strings.Builder
	for _, data := range cases {
		input.WriteString(nativeDump("O", data))
	}
	for _, line := range []string{
		"O 24:00:00.000 0000 80 60 00 00 # RTP_DUMP\n",
		"O 00:61:00.000 0000 80 60 00 00 # RTP_DUMP\n",
		"O 00:00:21.850 0000 80 60 gg 00 # RTP_DUMP\n",
		"O 00:00:21.850 0000 80 60 0 00 # RTP_DUMP\n",
		"O 00:00:21.850 0000 80\t60 00 00 # RTP_DUMP\n",
	} {
		input.WriteString(line)
	}
	input.WriteString(nativeDump("I", fixtureRTP()))
	var output bytes.Buffer
	result, err := ParseRTPLog(context.Background(), strings.NewReader(input.String()), &output, RTPLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Packets != 1 || result.MalformedRecords != int64(len(cases)+5) || !result.Truncated {
		t.Fatalf("malformed packets not disclosed: %+v", result)
	}
}

func TestRTPLogLimits(t *testing.T) {
	line := nativeDump("O", fixtureRTP())
	var reference bytes.Buffer
	_, err := ParseRTPLog(context.Background(), strings.NewReader(line), &reference, RTPLimits{})
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		limits  RTPLimits
		reason  string
		packets int64
	}{
		"packet":           {RTPLimits{MaxPackets: 1}, "max_packets", 1},
		"output":           {RTPLimits{MaxBytes: int64(reference.Len())}, "max_output_bytes", 1},
		"output_too_small": {RTPLimits{MaxBytes: 1}, "max_output_bytes", 0},
		"input":            {RTPLimits{MaxInputBytes: int64(len(line))}, "max_input_bytes", 1},
	} {
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			result, err := ParseRTPLog(context.Background(), strings.NewReader(line+line), &output, test.limits)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Truncated || result.StopReason != test.reason || result.Packets != test.packets {
				t.Fatalf("limit not disclosed: %+v", result)
			}
			if int64(len(decodeNativePackets(t, output.Bytes()))) != test.packets {
				t.Fatal("partial output record")
			}
		})
	}
}

func TestRTPOverlongLineIsDiscardedWithoutPoisoningNextRecord(t *testing.T) {
	line := nativeDump("I", fixtureRTP())
	input := strings.Repeat("a", 300000) + "\n" + line
	var output bytes.Buffer
	result, err := ParseRTPLog(context.Background(), strings.NewReader(input), &output, RTPLimits{MaxLineBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if result.Packets != 1 || result.OverlongLines != 1 || !result.Truncated || result.InputBytes != int64(len(input)) {
		t.Fatalf("overlong line was not bounded: %+v", result)
	}
}

func TestRTPPrefixAssociationRequiresAdjacency(t *testing.T) {
	prefix := "[1:2:0927/110121.850935:VERBOSE1:third_party/webrtc/pc/srtp_session.cc:549]"
	input := prefix + "\nintervening log record\n" + nativeDump("I", fixtureRTP()) + prefix + "\n\n\n\n" + nativeDump("I", fixtureRTP())
	var output bytes.Buffer
	result, err := ParseRTPLog(context.Background(), strings.NewReader(input), &output, RTPLimits{})
	if err != nil || result.TimestampedPackets != 0 || result.Packets != 2 {
		t.Fatalf("stale prefix association: %+v, %v", result, err)
	}
	for _, packet := range decodeNativePackets(t, output.Bytes()) {
		if packet.NativePrefix != "" {
			t.Fatal("stale prefix assigned to packet")
		}
	}
}

func TestRTPFinalLineAndErrors(t *testing.T) {
	var output bytes.Buffer
	result, err := ParseRTPLog(context.Background(), strings.NewReader(strings.TrimSuffix(nativeDump("I", fixtureRTP()), "\n")), &output, RTPLimits{})
	if err != nil || result.Packets != 1 || !result.Truncated || !result.UnterminatedFinalLine || result.StopReason != "unterminated_line" {
		t.Fatalf("missing final newline hidden: %+v %v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = ParseRTPLog(ctx, strings.NewReader(nativeDump("I", fixtureRTP())), io.Discard, RTPLimits{})
	if !errors.Is(err, context.Canceled) || result.StopReason != "canceled" || !result.Truncated {
		t.Fatal("cancel ignored")
	}
	if _, err := ParseRTPLog(context.Background(), strings.NewReader(""), io.Discard, RTPLimits{MaxBytes: -1}); err == nil {
		t.Fatal("invalid limits accepted")
	}
	result, err = ParseRTPLog(context.Background(), strings.NewReader(nativeDump("I", fixtureRTP())), rtpShortWriter{}, RTPLimits{})
	if !errors.Is(err, io.ErrShortWrite) || result.Packets != 0 || result.OutputBytes != 1 || !result.Truncated {
		t.Fatalf("partial writer hidden: %+v %v", result, err)
	}
}

type rtpShortWriter struct{}

func (rtpShortWriter) Write(data []byte) (int, error) { return 1, nil }
