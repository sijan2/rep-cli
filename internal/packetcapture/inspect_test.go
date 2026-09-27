package packetcapture

import (
	"encoding/binary"
	"os"
	"strings"
	"testing"
)

func TestInspectClassicEndianPrecisionAndCursor(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, nano := range []bool{false, true} {
			data := loopbackPacket(order, rtpShape())
			records := []fixturePacket{{data, uint32(len(data)), 1700000000, 123456}, {data[:38], uint32(len(data)), 1700000001, 1}}
			path := fixtureFile(t, fixtureBytes(order, nano, 0, records))
			page, err := Inspect(path, InspectOptions{Limit: 1})
			if err != nil {
				t.Fatal(err)
			}
			packet := page.Packets[0]
			if len(page.Packets) != 1 || !page.HasMore || page.NextOffset != 24+16+int64(len(data)) || page.ScannedBytes != 16+int64(len(data)) {
				t.Fatalf("bad first page: %+v", page)
			}
			if packet.SourceIP != "127.0.0.1" || packet.UDP == nil || packet.UDP.SourcePort != 32123 || packet.UDP.DestinationPort != 32124 || packet.UDP.CapturedPayloadBytes != 16 || packet.Truncated {
				t.Fatalf("bad UDP descriptor: %+v", packet)
			}
			if len(packet.Candidates) != 1 || packet.Candidates[0].Kind != "rtp_v2_header_candidate" || packet.Candidates[0].Verified {
				t.Fatalf("shape is not an unverified candidate: %+v", packet)
			}
			fraction := ".123456Z"
			if nano {
				fraction = ".000123456Z"
			}
			if !strings.HasSuffix(packet.Timestamp, fraction) {
				t.Fatalf("timestamp precision lost: %s", packet.Timestamp)
			}
			last, err := Inspect(path, InspectOptions{Offset: page.NextOffset})
			if err != nil {
				t.Fatal(err)
			}
			if last.HasMore || last.NextOffset != last.FileBytes || len(last.Packets) != 1 || !last.Packets[0].Truncated || last.Packets[0].UDP.CapturedPayloadBytes != 6 {
				t.Fatalf("bad last page: %+v", last)
			}
			if len(last.Packets[0].Candidates) != 0 {
				t.Fatal("guessed RTP with incomplete header")
			}
		}
	}
}

func TestInspectScanBudgetDoesNotSkipRecords(t *testing.T) {
	data := ipv4UDP(rtpShape())
	packet := fixturePacket{data, uint32(len(data)), 1, 0}
	path := fixtureFile(t, fixtureBytes(binary.LittleEndian, false, 101, []fixturePacket{packet, packet, packet}))
	if _, err := Inspect(path, InspectOptions{MaxScanBytes: int64(len(data)) + 15}); err == nil {
		t.Fatal("accepted scan budget without progress")
	}
	page, err := Inspect(path, InspectOptions{MaxScanBytes: int64(len(data)) + 16})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Packets) != 1 || !page.HasMore {
		t.Fatalf("bad bounded page: %+v", page)
	}
	last, err := Inspect(path, InspectOptions{Offset: page.NextOffset, MaxScanBytes: 2 * (int64(len(data)) + 16)})
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Packets) != 2 || last.HasMore || last.NextOffset != last.FileBytes {
		t.Fatalf("skipped packet: %+v", last)
	}
}

func TestInspectRejectsMalformedRecordsAndFormats(t *testing.T) {
	data := ipv4UDP(rtpShape())
	valid := fixtureBytes(binary.LittleEndian, false, 101, []fixturePacket{{data, uint32(len(data)), 1, 0}})
	cases := map[string][]byte{"short_global": valid[:20], "short_record": valid[:30], "short_payload": valid[:len(valid)-1]}
	mutate := func(f func([]byte)) []byte { copyData := append([]byte(nil), valid...); f(copyData); return copyData }
	cases["bad_timestamp"] = mutate(func(b []byte) { binary.LittleEndian.PutUint32(b[28:32], 1000000) })
	cases["caplen_exceeds_wire"] = mutate(func(b []byte) { binary.LittleEndian.PutUint32(b[36:40], 1) })
	cases["caplen_exceeds_snaplen"] = mutate(func(b []byte) { binary.LittleEndian.PutUint32(b[16:20], 1) })
	cases["zero_snaplen"] = mutate(func(b []byte) { binary.LittleEndian.PutUint32(b[16:20], 0) })
	cases["pcapng"] = mutate(func(b []byte) { copy(b[:4], []byte{0x0a, 0x0d, 0x0d, 0x0a}) })
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Inspect(fixtureFile(t, data), InspectOptions{}); err == nil {
				t.Fatal("accepted malformed evidence")
			}
		})
	}
	path := fixtureFile(t, valid)
	for _, options := range []InspectOptions{{Offset: 23}, {Offset: 99999}, {Limit: -1}, {Limit: MaxInspectPackets + 1}, {MaxScanBytes: 15}, {MaxScanBytes: MaxInspectScanBytes + 1}} {
		if _, err := Inspect(path, options); err == nil {
			t.Fatalf("accepted invalid bounds: %+v", options)
		}
	}
	if _, err := Inspect(t.TempDir(), InspectOptions{}); err == nil {
		t.Fatal("accepted directory")
	}
}

func TestInspectIPv6EthernetAndQUICCandidate(t *testing.T) {
	quic := []byte{0xc0, 0, 0, 0, 1, 2, 0xaa, 0xbb, 2, 0xcc, 0xdd, 0xff}
	packet := make([]byte, 14+40+8+len(quic))
	binary.BigEndian.PutUint16(packet[12:14], 0x86dd)
	ip := packet[14:]
	ip[0] = 0x60
	binary.BigEndian.PutUint16(ip[4:6], uint16(8+len(quic)))
	ip[6] = 17
	ip[7] = 64
	ip[23] = 1
	ip[39] = 1
	udp := ip[40:]
	binary.BigEndian.PutUint16(udp[:2], 12001)
	binary.BigEndian.PutUint16(udp[2:4], 12002)
	binary.BigEndian.PutUint16(udp[4:6], uint16(len(udp)))
	copy(udp[8:], quic)
	page, err := Inspect(fixtureFile(t, fixtureBytes(binary.BigEndian, false, 1, []fixturePacket{{packet, uint32(len(packet)), 1, 0}})), InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p := page.Packets[0]
	if p.Network != "ipv6" || p.SourceIP != "::1" || p.DestinationIP != "::1" || p.UDP == nil || p.UDP.DestinationPort != 12002 || len(p.Candidates) != 1 || p.Candidates[0].Kind != "quic_long_header_candidate" || p.Candidates[0].Verified {
		t.Fatalf("invalid IPv6/QUIC candidate: %+v", p)
	}
	// UDP ports alone, including 443, are never used as protocol identity.
	data := ipv4UDP([]byte{0x40, 0xff, 0, 1})
	binary.BigEndian.PutUint16(data[22:24], 443)
	page, err = Inspect(fixtureFile(t, fixtureBytes(binary.LittleEndian, false, 101, []fixturePacket{{data, uint32(len(data)), 1, 0}})), InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Packets[0].Candidates) != 0 {
		t.Fatal("guessed QUIC from port or short-header bits")
	}
}

func TestInspectFragmentsAndUnsupportedLinkDoNotGuessPayload(t *testing.T) {
	data := ipv4UDP(rtpShape())
	binary.BigEndian.PutUint16(data[6:8], 0x2000)
	page, err := Inspect(fixtureFile(t, fixtureBytes(binary.LittleEndian, false, 101, []fixturePacket{{data, uint32(len(data)), 1, 0}})), InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !page.Packets[0].Fragmented || page.Packets[0].UDP != nil || len(page.Packets[0].Candidates) != 0 {
		t.Fatalf("interpreted fragment as full datagram: %+v", page.Packets[0])
	}
	page, err = Inspect(fixtureFile(t, fixtureBytes(binary.LittleEndian, false, 9999, []fixturePacket{{data, uint32(len(data)), 1, 0}})), InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Packets[0].Notes) != 1 || page.Packets[0].Notes[0] != "unsupported_link_type" {
		t.Fatalf("missing unsupported link disclosure: %+v", page)
	}
}

func BenchmarkInspectBoundedPage(b *testing.B) {
	data := ipv4UDP(rtpShape())
	records := make([]fixturePacket, 10000)
	for i := range records {
		records[i] = fixturePacket{data, uint32(len(data)), 1700000000, uint32(i)}
	}
	path := fixtureFile(b, fixtureBytes(binary.LittleEndian, false, 101, records))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Inspect(path, InspectOptions{Offset: 24 + 5000*int64(16+len(data)), Limit: 20}); err != nil {
			b.Fatal(err)
		}
	}
}

func TestInspectEmptyClassicFile(t *testing.T) {
	path := fixtureFile(t, fixtureBytes(binary.LittleEndian, false, 0, nil))
	page, err := Inspect(path, InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if page.HasMore || page.NextOffset != 24 || len(page.Packets) != 0 || !page.PayloadOmitted {
		t.Fatalf("bad empty page: %+v", page)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}
