//go:build darwin

package headless

import (
	"bytes"
	"encoding/binary"
	"strings"

	"golang.org/x/sys/unix"
)

// processStartMicros reads the kernel's process start time. Every task-scoped
// browser command verifies ownership, so avoiding a ps exec here matters.
func processStartMicros(pid int) int64 {
	if pid <= 0 {
		return 0
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info == nil || int(info.Proc.P_pid) != pid {
		return 0
	}
	start := info.Proc.P_starttime
	return int64(start.Sec)*1_000_000 + int64(start.Usec)
}

// processCommand returns argv joined by spaces, the form `ps -o command=`
// prints. kern.procargs2 holds argc, the executable path, NUL padding, then
// NUL-separated arguments and environment.
func processCommand(pid int) (string, bool) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(raw) < 4 {
		return "", false
	}
	argc := int(binary.LittleEndian.Uint32(raw[:4]))
	rest := raw[4:]
	end := bytes.IndexByte(rest, 0)
	if argc <= 0 || argc > 4096 || end < 0 {
		return "", false
	}
	rest = bytes.TrimLeft(rest[end:], "\x00")
	args := make([]string, 0, argc)
	for len(args) < argc && len(rest) > 0 {
		end = bytes.IndexByte(rest, 0)
		if end < 0 {
			return "", false
		}
		args = append(args, string(rest[:end]))
		rest = rest[end+1:]
	}
	if len(args) != argc {
		return "", false
	}
	return strings.Join(args, " "), true
}
