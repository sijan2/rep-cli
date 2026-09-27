//go:build unix

package packetcapture

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestInspectRejectsFIFOWithoutOpening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipe.pcap")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(path, InspectOptions{}); err == nil {
		t.Fatal("accepted FIFO")
	}
}
