//go:build !unix

package packetcapture

import "os"

func openReadOnly(path string) (*os.File, error) { return os.Open(path) }
