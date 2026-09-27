//go:build !darwin && !linux && !dragonfly && !freebsd && !netbsd && !openbsd

package evidence

import (
	"fmt"
	"os"
)

func lockFile(f *os.File) error {
	return fmt.Errorf("evidence journal locking is unsupported on this platform")
}
func unlockFile(f *os.File) error { return nil }
