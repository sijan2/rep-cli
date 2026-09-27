//go:build darwin && cgo

package packetcapture

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func replayFixture(t testing.TB, input string, options Options, ctx context.Context) (nativeResult, []byte) {
	t.Helper()
	file, err := os.OpenFile(options.Output, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	result, err := replayNative(ctx, options, file, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(options.Output)
	if err != nil {
		t.Fatal(err)
	}
	return result, output
}

func TestNativeWriterExactBytesLengthsAndTimestamps(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		first := loopbackPacket(order, rtpShape())
		second := []byte{0, 0xff, 1, 2, 3, 4, 5}
		records := []fixturePacket{{first, uint32(len(first)), 1700000000, 123456}, {second, 99, 1700000001, 789}}
		input := fixtureFile(t, fixtureBytes(order, false, 0, records))
		options := captureOptions(t)
		options.Filter = "len >= 0" // Preserve every synthetic record, including opaque bytes.
		result, written := replayFixture(t, input, options, context.Background())
		if result.Packets != 2 || result.CapturedBytes != uint64(len(first)+len(second)) || result.OriginalBytes != uint64(len(first)+99) || result.TruncatedPackets != 1 || result.StopReason != "offline_end" || !result.PCAPValid || result.Stats.Available {
			t.Fatalf("bad native counters: %+v", result)
		}
		// The container is written in native byte order. Packet bytes and record
		// timestamp/length values survive exactly, including an embedded NUL.
		writtenOrder, _, _, err := pcapFormat(written[:24])
		if err != nil {
			t.Fatal(err)
		}
		at := 24
		for _, record := range records {
			if writtenOrder.Uint32(written[at:at+4]) != record.seconds || writtenOrder.Uint32(written[at+4:at+8]) != record.fraction || writtenOrder.Uint32(written[at+8:at+12]) != uint32(len(record.data)) || writtenOrder.Uint32(written[at+12:at+16]) != record.wire || !bytes.Equal(written[at+16:at+16+len(record.data)], record.data) {
				t.Fatalf("native writer changed record at %d", at)
			}
			at += 16 + len(record.data)
		}
		if at != len(written) {
			t.Fatal("unexpected trailing bytes")
		}
	}
}

func TestNativeReadinessRequiresInstalledFilterAndFlushedHeader(t *testing.T) {
	input := fixtureFile(t, fixtureBytes(binary.LittleEndian, false, 0, nil))
	for _, valid := range []bool{true, false} {
		options := captureOptions(t)
		if !valid {
			options.Filter = "invalid ((( filter"
		}
		file, err := os.OpenFile(options.Output, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		var headerBytes int64
		options.Ready = func() {
			calls++
			if info, err := file.Stat(); err == nil {
				headerBytes = info.Size()
			}
		}
		_, runErr := replayNative(context.Background(), options, file, input)
		_ = file.Close()
		if valid && (runErr != nil || calls != 1 || headerBytes < 24) {
			t.Fatalf("readiness before valid writer: calls=%d bytes=%d err=%v", calls, headerBytes, runErr)
		}
		if !valid && (runErr == nil || calls != 0) {
			t.Fatalf("failed setup signalled readiness: calls=%d err=%v", calls, runErr)
		}
	}
}

func TestNativeWriterLimitsDoNotWritePartialPacket(t *testing.T) {
	data := loopbackPacket(binary.LittleEndian, rtpShape())
	record := fixturePacket{data, uint32(len(data)), 1, 0}
	input := fixtureFile(t, fixtureBytes(binary.LittleEndian, false, 0, []fixturePacket{record, record, record}))
	for _, test := range []struct {
		name, reason string
		maxBytes     uint64
		maxPackets   uint64
		want         uint64
	}{
		{"header_only", "byte_limit", 24, 100, 0},
		{"too_small_for_first", "byte_limit", 24 + 16 + uint64(len(data)) - 1, 100, 0},
		{"one_exact", "byte_limit", 24 + 16 + uint64(len(data)), 100, 1},
		{"second_would_exceed", "byte_limit", 24 + 2*(16+uint64(len(data))) - 1, 100, 1},
		{"packet_limit", "packet_limit", 1 << 20, 1, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := captureOptions(t)
			options.MaxBytes = test.maxBytes
			options.MaxPackets = test.maxPackets
			result, output := replayFixture(t, input, options, context.Background())
			if result.StopReason != test.reason || result.Packets != test.want || uint64(len(output)) != 24+test.want*(16+uint64(len(data))) {
				t.Fatalf("native bound violated: %+v, %d bytes", result, len(output))
			}
			page, err := Inspect(options.Output, InspectOptions{})
			if err != nil || uint64(len(page.Packets)) != test.want {
				t.Fatalf("partial pcap record: %+v %v", page, err)
			}
		})
	}
}

func TestNativeWriterCancellationAndDeadline(t *testing.T) {
	data := loopbackPacket(binary.LittleEndian, rtpShape())
	input := fixtureFile(t, fixtureBytes(binary.LittleEndian, false, 0, []fixturePacket{{data, uint32(len(data)), 1, 0}}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, output := replayFixture(t, input, captureOptions(t), ctx)
	if result.StopReason != "cancelled" || result.Packets != 0 || len(output) != 24 {
		t.Fatalf("native cancellation ignored: %+v", result)
	}
	options := captureOptions(t)
	options.Duration = 0 // private replay exercises the actual C deadline check.
	result, output = replayFixture(t, input, options, context.Background())
	if result.StopReason != "duration_limit" || result.Packets != 0 || len(output) != 24 {
		t.Fatalf("native deadline ignored: %+v", result)
	}
}

func TestNativeWriterDrainsManyBatches(t *testing.T) {
	data := bytes.Repeat([]byte{0x00, 0xff, 0x80, 0x01}, 256)
	records := make([]fixturePacket, 10000)
	for i := range records {
		records[i] = fixturePacket{data, uint32(len(data)), 1700000000, uint32(i)}
	}
	input := fixtureFile(t, fixtureBytes(binary.LittleEndian, false, 1, records))
	options := captureOptions(t)
	options.Filter = "len >= 0"
	options.Duration = 2 * time.Second
	result, written := replayFixture(t, input, options, context.Background())
	if result.Packets != uint64(len(records)) || result.StopReason != "offline_end" || len(written) != 24+len(records)*(16+len(data)) {
		t.Fatalf("lost buffered batches: %+v", result)
	}
}

func TestNativeFilterExcludesOtherTrafficBeforeWriting(t *testing.T) {
	accepted := loopbackPacket(binary.LittleEndian, rtpShape())
	rejected := append([]byte(nil), accepted...)
	binary.BigEndian.PutUint16(rejected[26:28], 54321) // Different UDP destination port.
	records := make([]fixturePacket, 0, 500)
	for i := 0; i < 250; i++ {
		records = append(records, fixturePacket{rejected, uint32(len(rejected)), 1, 0}, fixturePacket{accepted, uint32(len(accepted)), 1, 0})
	}
	input := fixtureFile(t, fixtureBytes(binary.LittleEndian, false, 0, records))
	options := captureOptions(t)
	result, output := replayFixture(t, input, options, context.Background())
	if result.Packets != 250 || result.StopReason != "offline_end" || len(output) != 24+250*(16+len(accepted)) {
		t.Fatalf("filter retained unrelated traffic: %+v, %d bytes", result, len(output))
	}
	page, err := Inspect(options.Output, InspectOptions{Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	for _, packet := range page.Packets {
		if packet.UDP == nil || packet.UDP.DestinationPort != 32124 {
			t.Fatalf("unexpected packet passed native BPF: %+v", packet)
		}
	}
}

func TestNativeInvalidFilterFailsBeforeObservation(t *testing.T) {
	input := fixtureFile(t, fixtureBytes(binary.LittleEndian, false, 0, nil))
	options := captureOptions(t)
	options.Filter = "udp and ("
	file, err := os.Create(options.Output)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	result, err := replayNative(context.Background(), options, file, input)
	stat, statErr := file.Stat()
	if err == nil || !strings.Contains(err.Error(), "invalid BPF filter") || result.PCAPValid || result.ObservationStartedAt != nil || result.Packets != 0 || statErr != nil || stat.Size() != 0 {
		t.Fatalf("filter failure appears observed: %+v, %v, %v", result, err, statErr)
	}
}

func TestNativeInputErrorRetainsValidCapturedPrefix(t *testing.T) {
	packet := loopbackPacket(binary.LittleEndian, rtpShape())
	record := fixturePacket{packet, uint32(len(packet)), 1, 0}
	data := fixtureBytes(binary.LittleEndian, false, 0, []fixturePacket{record, record})
	input := fixtureFile(t, data[:len(data)-1])
	options := captureOptions(t)
	file, err := os.Create(options.Output)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	result, err := replayNative(context.Background(), options, file, input)
	if err == nil || result.StopReason != "read_error" || result.Packets != 1 || !result.PCAPValid || result.ObservationStartedAt == nil {
		t.Fatalf("read failure lost captured prefix: %+v, %v", result, err)
	}
	page, err := Inspect(options.Output, InspectOptions{})
	if err != nil || len(page.Packets) != 1 || page.HasMore {
		t.Fatalf("retained prefix is invalid: %+v, %v", page, err)
	}
}

func TestNativeFlushFailureIsInvalidAndDoesNotCloseCallerDescriptor(t *testing.T) {
	packet := loopbackPacket(binary.LittleEndian, rtpShape())
	input := fixtureFile(t, fixtureBytes(binary.LittleEndian, false, 0, []fixturePacket{{packet, uint32(len(packet)), 1, 0}}))
	readonly, err := os.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	result, err := replayNative(context.Background(), captureOptions(t), readonly, input)
	if err == nil || result.StopReason != "write_error" || result.PCAPValid {
		t.Fatalf("failed flush marked valid: %+v, %v", result, err)
	}
	if _, err := readonly.Stat(); err != nil {
		t.Fatalf("native cleanup closed caller's descriptor: %v", err)
	}
}

func BenchmarkNativeOfflineWriter(b *testing.B) {
	data := bytes.Repeat([]byte{0, 0xff, 0x80, 1}, 256)
	records := make([]fixturePacket, 10000)
	for i := range records {
		records[i] = fixturePacket{data, uint32(len(data)), 1700000000, uint32(i)}
	}
	input := fixtureFile(b, fixtureBytes(binary.LittleEndian, false, 1, records))
	options := captureOptions(b)
	options.Filter = "len >= 0"
	file, err := os.OpenFile(filepath.Join(b.TempDir(), "writer.pcap"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		b.Fatal(err)
	}
	defer file.Close()
	b.SetBytes(int64(len(records) * len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := file.Truncate(0); err != nil {
			b.Fatal(err)
		}
		if _, err := file.Seek(0, 0); err != nil {
			b.Fatal(err)
		}
		result, err := replayNative(context.Background(), options, file, input)
		if err != nil || result.Packets != 10000 {
			b.Fatalf("replay: %+v %v", result, err)
		}
	}
}
