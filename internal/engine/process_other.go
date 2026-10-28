//go:build !unix

package engine

import "os/exec"

func engineConfigureProcess(command *exec.Cmd) {}

func engineStopProcess(command *exec.Cmd) {
	if command.Process != nil {
		_ = command.Process.Kill()
	}
}
