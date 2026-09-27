//go:build !darwin

package headless

// Other platforms verify ownership through ps.
func processStartMicros(int) int64 { return 0 }

func processCommand(int) (string, bool) { return "", false }
