//go:build unix

package packetcapture

import (
	"os"
	"syscall"
)

// Nonblocking open avoids a FIFO swap between the preliminary Stat and Open.
// O_NONBLOCK has no effect on reads from the required regular file.
func openReadOnly(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
