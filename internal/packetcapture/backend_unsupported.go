//go:build !darwin || !cgo

package packetcapture

import (
	"context"
	"os"
)

func Supported() bool                  { return false }
func backendVersion() string           { return "unsupported" }
func Interfaces() ([]Interface, error) { return nil, ErrUnsupported }
func captureNative(context.Context, Options, *os.File) (nativeResult, error) {
	return nativeResult{}, ErrUnsupported
}
