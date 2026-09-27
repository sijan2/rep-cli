//go:build darwin

package headless

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// The sysctl readers replace ps on every task-scoped command; they must agree
// with the ps output that ownership checks were designed around.
func TestKernelProcessIdentityMatchesPS(t *testing.T) {
	child := exec.Command("/bin/sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	pid := child.Process.Pid
	command, ok := processCommand(pid)
	if !ok || command != "/bin/sleep 30" {
		t.Fatalf("argv = %q, %v", command, ok)
	}
	output, err := exec.Command("/bin/ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil || strings.TrimSpace(string(output)) != command {
		t.Fatalf("ps command %q differs from kernel argv %q (%v)", output, command, err)
	}
	start := processStartMicros(pid)
	if start == 0 || start != processStartMicros(pid) {
		t.Fatalf("start time is unavailable or unstable: %d", start)
	}
	if processStartMicros(os.Getpid()) == start {
		t.Fatal("distinct processes reported the same start time")
	}
	if _, ok := processCommand(-1); ok || processStartMicros(-1) != 0 {
		t.Fatal("invalid pid returned an identity")
	}
}

func TestOwnershipUsesKernelIdentityAndRejectsReuse(t *testing.T) {
	profile := "/tmp/rep-owned-profile"
	// The shell keeps these browser-shaped arguments in its own argv; "; :"
	// prevents it from exec-replacing itself with sleep.
	child := exec.Command("/bin/sh", "-c", "sleep 30; :", "--headless=new", "--user-data-dir="+profile)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	pid := child.Process.Pid
	state := State{PID: pid, Profile: profile, ProcessStamp: processStamp(pid), ProcessStart: processStartMicros(pid)}
	if !ownedProcess(state) {
		t.Fatal("kernel identity did not verify the owned process")
	}
	reused := state
	reused.ProcessStart++
	reused.ProcessStamp = "Thu Jan  1 00:00:00 1970"
	if ownedProcess(reused) {
		t.Fatal("a different start time was accepted as the owned process")
	}
	legacy := state
	legacy.ProcessStart = 0
	if !ownedProcess(legacy) {
		t.Fatal("states without kernel identity must keep the ps path")
	}
	other := state
	other.Profile = "/tmp/rep-other-profile"
	if ownedProcess(other) {
		t.Fatal("another profile was accepted")
	}
}
