package packetcapture

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

type fixturePacket struct {
	data              []byte
	wire              uint32
	seconds, fraction uint32
}

func fixtureBytes(order binary.ByteOrder, nano bool, link uint32, records []fixturePacket) []byte {
	data := make([]byte, 24)
	magic := uint32(0xa1b2c3d4)
	if nano {
		magic = 0xa1b23c4d
	}
	order.PutUint32(data[:4], magic)
	order.PutUint16(data[4:6], 2)
	order.PutUint16(data[6:8], 4)
	order.PutUint32(data[16:20], 65535)
	order.PutUint32(data[20:24], link)
	for _, record := range records {
		header := make([]byte, 16)
		order.PutUint32(header[:4], record.seconds)
		order.PutUint32(header[4:8], record.fraction)
		order.PutUint32(header[8:12], uint32(len(record.data)))
		order.PutUint32(header[12:16], record.wire)
		data = append(data, header...)
		data = append(data, record.data...)
	}
	return data
}

func fixtureFile(t testing.TB, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic.pcap")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func ipv4UDP(payload []byte) []byte {
	packet := make([]byte, 28+len(payload))
	packet[0] = 0x45
	packet[8] = 64
	packet[9] = 17
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	copy(packet[12:16], []byte{127, 0, 0, 1})
	copy(packet[16:20], []byte{127, 0, 0, 1})
	binary.BigEndian.PutUint16(packet[20:22], 32123)
	binary.BigEndian.PutUint16(packet[22:24], 32124)
	binary.BigEndian.PutUint16(packet[24:26], uint16(8+len(payload)))
	copy(packet[28:], payload)
	var sum uint32
	for at := 0; at < 20; at += 2 {
		sum += uint32(binary.BigEndian.Uint16(packet[at : at+2]))
	}
	sum = (sum & 0xffff) + (sum >> 16)
	sum += (sum >> 16)
	binary.BigEndian.PutUint16(packet[10:12], ^uint16(sum))
	return packet
}

func loopbackPacket(order binary.ByteOrder, payload []byte) []byte {
	packet := make([]byte, 4)
	order.PutUint32(packet, 2)
	return append(packet, ipv4UDP(payload)...)
}

func rtpShape() []byte {
	return []byte{0x80, 96, 0x12, 0x34, 0, 0, 0, 1, 0x01, 0x02, 0x03, 0x04, 0xff, 0, 0x41, 0x42}
}
