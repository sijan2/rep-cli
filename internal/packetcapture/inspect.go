package packetcapture

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Inspect reads record headers and at most 4096 header bytes per packet. It
// never loads a full capture or returns packet payloads. Offset is a record
// boundary supplied by the caller, normally a previous page's NextOffset.
func Inspect(path string, options InspectOptions) (Inspection, error) {
	if options.Offset == 0 {
		options.Offset = 24
	}
	if options.Limit == 0 {
		options.Limit = 20
	}
	if options.MaxScanBytes == 0 {
		options.MaxScanBytes = 1 << 20
	}
	if options.Offset < 24 || options.Limit < 1 || options.Limit > MaxInspectPackets || options.MaxScanBytes < 16 || options.MaxScanBytes > MaxInspectScanBytes {
		return Inspection{}, fmt.Errorf("offset must be a pcap record boundary >=24, limit 1..%d, and max-scan-bytes 16..%d", MaxInspectPackets, MaxInspectScanBytes)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Inspection{}, err
	}
	before, err := os.Stat(absolute)
	if err != nil {
		return Inspection{}, err
	}
	if !before.Mode().IsRegular() {
		return Inspection{}, errors.New("pcap input must be a regular file")
	}
	file, err := openReadOnly(absolute)
	if err != nil {
		return Inspection{}, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return Inspection{}, err
	}
	if !stat.Mode().IsRegular() {
		return Inspection{}, errors.New("pcap input must be a regular file")
	}
	if !os.SameFile(before, stat) {
		return Inspection{}, errors.New("pcap changed before inspection; retry on a completed artifact")
	}
	var global [24]byte
	if _, err = file.ReadAt(global[:], 0); err != nil {
		return Inspection{}, fmt.Errorf("read classic pcap header: %w", err)
	}
	order, resolution, scale, err := pcapFormat(global[:])
	if err != nil {
		return Inspection{}, err
	}
	if order.Uint16(global[4:6]) != 2 || order.Uint16(global[6:8]) != 4 {
		return Inspection{}, errors.New("unsupported pcap version; expected classic pcap 2.4")
	}
	snaplen := order.Uint32(global[16:20])
	if snaplen == 0 || snaplen > 16<<20 {
		return Inspection{}, errors.New("invalid or unsupported pcap snaplen (maximum 16 MiB)")
	}
	// The upper network bits carry optional FCS metadata, not the link type.
	link := linkType(int(order.Uint32(global[20:24]) & 0xffff))
	if options.Offset > stat.Size() {
		return Inspection{}, errors.New("pcap offset is beyond the file")
	}
	result := Inspection{Path: absolute, FileBytes: stat.Size(), ModifiedAt: stat.ModTime().UTC(), Format: "classic_pcap",
		TimestampResolution: resolution, Datalink: link, Snaplen: snaplen, Offset: options.Offset, NextOffset: options.Offset,
		Packets: []Packet{}, PayloadOmitted: true, Interpretation: "Header inspection only. Protocol candidates are unverified; encrypted bytes remain encrypted. Page end is not evidence of capture completeness. Offset must come from a record boundary."}
	result.ByteOrder = "little_endian"
	if order == binary.BigEndian {
		result.ByteOrder = "big_endian"
	}
	var record [16]byte
	var prefix [4096]byte
	for len(result.Packets) < options.Limit && result.NextOffset < stat.Size() {
		offset := result.NextOffset
		if stat.Size()-offset < 16 {
			return Inspection{}, fmt.Errorf("incomplete pcap record header at offset %d", offset)
		}
		if _, err = file.ReadAt(record[:], offset); err != nil {
			return Inspection{}, err
		}
		seconds, fraction := order.Uint32(record[:4]), order.Uint32(record[4:8])
		caplen, wirelen := order.Uint32(record[8:12]), order.Uint32(record[12:16])
		if fraction >= uint32(1000000000/scale) {
			return Inspection{}, fmt.Errorf("invalid packet timestamp at offset %d", offset)
		}
		if caplen > snaplen || caplen > wirelen {
			return Inspection{}, fmt.Errorf("invalid captured/original length at offset %d", offset)
		}
		size := int64(caplen) + 16
		if size > stat.Size()-offset {
			return Inspection{}, fmt.Errorf("incomplete pcap packet bytes at offset %d", offset)
		}
		if size > options.MaxScanBytes-result.ScannedBytes {
			if len(result.Packets) == 0 {
				return Inspection{}, fmt.Errorf("packet at offset %d needs %d scan bytes; increase --max-scan-bytes", offset, size)
			}
			break
		}
		packet := Packet{Offset: offset, NextOffset: offset + size, Timestamp: time.Unix(int64(seconds), int64(fraction)*scale).UTC().Format(time.RFC3339Nano),
			CapturedLength: caplen, OriginalLength: wirelen, Truncated: caplen < wirelen}
		length := min(int(caplen), len(prefix))
		if length > 0 {
			if _, err = file.ReadAt(prefix[:length], offset+16); err != nil {
				return Inspection{}, err
			}
			describePacket(&packet, prefix[:length], link.ID, order)
		}
		result.Packets = append(result.Packets, packet)
		result.NextOffset += size
		result.ScannedBytes += size
	}
	result.HasMore = result.NextOffset < stat.Size()
	after, err := file.Stat()
	if err != nil {
		return Inspection{}, err
	}
	current, err := os.Stat(absolute)
	if err != nil || !os.SameFile(stat, current) || stat.Size() != after.Size() || !stat.ModTime().Equal(after.ModTime()) {
		return Inspection{}, errors.New("pcap changed during inspection; retry on a completed artifact")
	}
	return result, nil
}

func pcapFormat(header []byte) (binary.ByteOrder, string, int64, error) {
	switch binary.BigEndian.Uint32(header[:4]) {
	case 0xd4c3b2a1:
		return binary.LittleEndian, "microseconds", 1000, nil
	case 0xa1b2c3d4:
		return binary.BigEndian, "microseconds", 1000, nil
	case 0x4d3cb2a1:
		return binary.LittleEndian, "nanoseconds", 1, nil
	case 0xa1b23c4d:
		return binary.BigEndian, "nanoseconds", 1, nil
	case 0x0a0d0d0a:
		return nil, "", 0, errors.New("pcapng is not supported by this bounded inspector; use a classic .pcap export")
	default:
		return nil, "", 0, errors.New("unrecognized classic pcap magic")
	}
}

func linkType(id int) LinkType {
	names := map[int]string{0: "NULL", 1: "EN10MB", 12: "RAW", 101: "RAW", 108: "LOOP", 113: "LINUX_SLL", 276: "LINUX_SLL2"}
	name := names[id]
	if name == "" {
		name = "unknown"
	}
	return LinkType{ID: id, Name: name}
}

func describePacket(packet *Packet, data []byte, link int, order binary.ByteOrder) {
	networkOffset, protocol := 0, uint16(0)
	switch link {
	case 0, 108:
		if len(data) < 4 {
			packet.Notes = append(packet.Notes, "short_loopback_header")
			return
		}
		if link == 108 {
			order = binary.BigEndian
		}
		family := order.Uint32(data[:4])
		switch family {
		case 2:
			protocol = 0x0800
		case 10, 24, 28, 30:
			protocol = 0x86dd
		default:
			packet.Notes = append(packet.Notes, "unsupported_loopback_family")
			return
		}
		networkOffset = 4
	case 1:
		if len(data) < 14 {
			packet.Notes = append(packet.Notes, "short_ethernet_header")
			return
		}
		networkOffset, protocol = 14, binary.BigEndian.Uint16(data[12:14])
		for count := 0; protocol == 0x8100 || protocol == 0x88a8 || protocol == 0x9100; count++ {
			if count >= 4 || len(data) < networkOffset+4 {
				packet.Notes = append(packet.Notes, "vlan_header_unavailable_or_limit")
				return
			}
			protocol = binary.BigEndian.Uint16(data[networkOffset+2 : networkOffset+4])
			networkOffset += 4
		}
	case 12, 101:
		if len(data) == 0 {
			return
		}
		if data[0]>>4 == 4 {
			protocol = 0x0800
		} else if data[0]>>4 == 6 {
			protocol = 0x86dd
		}
	case 113:
		if len(data) < 16 {
			packet.Notes = append(packet.Notes, "short_linux_sll_header")
			return
		}
		networkOffset, protocol = 16, binary.BigEndian.Uint16(data[14:16])
	case 276:
		if len(data) < 20 {
			packet.Notes = append(packet.Notes, "short_linux_sll2_header")
			return
		}
		networkOffset, protocol = 20, binary.BigEndian.Uint16(data[:2])
	default:
		packet.Notes = append(packet.Notes, "unsupported_link_type")
		return
	}
	if len(data) <= networkOffset {
		packet.Notes = append(packet.Notes, "network_header_unavailable")
		return
	}
	ip := data[networkOffset:]
	transportOffset, transportBytes, next := 0, 0, byte(0)
	switch protocol {
	case 0x0800:
		packet.Network = "ipv4"
		if len(ip) < 20 || ip[0]>>4 != 4 {
			packet.Notes = append(packet.Notes, "short_or_invalid_ipv4_header")
			return
		}
		header, total := int(ip[0]&15)*4, int(binary.BigEndian.Uint16(ip[2:4]))
		if header < 20 || header > len(ip) || total < header {
			packet.Notes = append(packet.Notes, "invalid_or_unavailable_ipv4_header")
			return
		}
		if int64(total) > int64(packet.OriginalLength)-int64(networkOffset) {
			packet.Notes = append(packet.Notes, "ip_length_exceeds_original_packet")
			return
		}
		packet.SourceIP, packet.DestinationIP = net.IP(ip[12:16]).String(), net.IP(ip[16:20]).String()
		fragment := binary.BigEndian.Uint16(ip[6:8])
		packet.Fragmented = fragment&0x3fff != 0
		if packet.Fragmented {
			packet.Notes = append(packet.Notes, "fragment_reassembly_not_performed")
			return
		}
		next, transportOffset, transportBytes = ip[9], header, total-header
	case 0x86dd:
		packet.Network = "ipv6"
		if len(ip) < 40 || ip[0]>>4 != 6 {
			packet.Notes = append(packet.Notes, "short_or_invalid_ipv6_header")
			return
		}
		packet.SourceIP, packet.DestinationIP = net.IP(ip[8:24]).String(), net.IP(ip[24:40]).String()
		transportBytes = int(binary.BigEndian.Uint16(ip[4:6]))
		if int64(transportBytes)+40 > int64(packet.OriginalLength)-int64(networkOffset) {
			packet.Notes = append(packet.Notes, "ip_length_exceeds_original_packet")
			return
		}
		next, transportOffset = ip[6], 40
		if transportBytes == 0 {
			packet.Notes = append(packet.Notes, "ipv6_empty_payload_or_jumbogram_not_inspected")
			return
		}
		for count := 0; next == 0 || next == 43 || next == 60 || next == 44 || next == 51; count++ {
			if count >= 8 || len(ip) < transportOffset+8 || transportBytes < 8 {
				packet.Notes = append(packet.Notes, "ipv6_extension_header_unavailable_or_limit")
				return
			}
			ext := ip[transportOffset:]
			size := (int(ext[1]) + 1) * 8
			if next == 44 {
				size = 8
				packet.Fragmented = binary.BigEndian.Uint16(ext[2:4])&0xfff9 != 0
				if packet.Fragmented {
					packet.Notes = append(packet.Notes, "fragment_reassembly_not_performed")
					return
				}
			} else if next == 51 {
				size = (int(ext[1]) + 2) * 4
			}
			if size > transportBytes || len(ip) < transportOffset+size {
				packet.Notes = append(packet.Notes, "ipv6_extension_header_unavailable")
				return
			}
			next = ext[0]
			transportOffset += size
			transportBytes -= size
		}
	default:
		packet.Notes = append(packet.Notes, "unsupported_network_protocol")
		return
	}
	if next == 6 {
		packet.Transport = "tcp"
		return
	}
	if next == 50 {
		packet.Transport = "esp"
		packet.Notes = append(packet.Notes, "encrypted_transport_not_decoded")
		return
	}
	if next != 17 {
		return
	}
	packet.Transport = "udp"
	if len(ip) < transportOffset+8 || transportBytes < 8 {
		packet.Notes = append(packet.Notes, "udp_header_unavailable")
		return
	}
	udp := ip[transportOffset:]
	length := binary.BigEndian.Uint16(udp[4:6])
	if length < 8 || int(length) > transportBytes {
		packet.Notes = append(packet.Notes, "invalid_udp_length")
		return
	}
	available := max(int64(0), int64(packet.CapturedLength)-int64(networkOffset+transportOffset+8))
	packet.UDP = &UDPHeader{SourcePort: binary.BigEndian.Uint16(udp[:2]), DestinationPort: binary.BigEndian.Uint16(udp[2:4]), Length: length, CapturedPayloadBytes: min(available, int64(length)-8)}
	payload := udp[8:min(len(udp), int(length))]
	describeCandidates(packet, payload, int(length)-8)
}

func describeCandidates(packet *Packet, payload []byte, declared int) {
	if len(payload) >= 7 && payload[0]&0xc0 == 0xc0 && binary.BigEndian.Uint32(payload[1:5]) != 0 {
		dcid := int(payload[5])
		scidOffset := 6 + dcid
		if dcid <= 20 && scidOffset < len(payload) {
			scid := int(payload[scidOffset])
			if scid <= 20 && scidOffset+1+scid <= len(payload) {
				packet.Candidates = append(packet.Candidates, Candidate{Kind: "quic_long_header_candidate", Basis: "long-header bit pattern and bounded connection-ID lengths only", Verified: false})
			}
		}
	}
	if len(payload) >= 12 && payload[0]>>6 == 2 {
		header := 12 + int(payload[0]&15)*4
		if header > len(payload) || header > declared {
			return
		}
		if payload[0]&0x10 != 0 {
			if header+4 > len(payload) {
				return
			}
			header += 4 + int(binary.BigEndian.Uint16(payload[header+2:header+4]))*4
			if header > declared {
				return
			}
		}
		packet.Candidates = append(packet.Candidates, Candidate{Kind: "rtp_v2_header_candidate", Basis: "version and header shape only; RTP, SRTP, RTCP, and application bytes are not identified", Verified: false})
	}
}
