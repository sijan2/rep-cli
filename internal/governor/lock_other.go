//go:build !darwin && !linux && !dragonfly && !freebsd && !netbsd && !openbsd

package governor

import (
	"fmt"
	"os"
)

// Without flock exclusivity cannot be proven, so a run is refused rather than
// allowed to double the rate on a site.
func tryLockFile(f *os.File) error {
	return fmt.Errorf("site locking is unsupported on this platform")
}

func unlockFile(f *os.File) error { return nil }
