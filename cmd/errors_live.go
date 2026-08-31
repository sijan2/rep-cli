package cmd

import (
	"fmt"
	"os"

	"github.com/repplus/rep-cli/internal/output"
)

// emitLiveUnavailable is the shared exit path when live.json can't be read.
// Every agent-facing command hits this at cold start; centralizing keeps the
// error code, message, and suggestions identical across the CLI surface.
func emitLiveUnavailable(command string, err error) error {
	ae := output.WrapError(
		err,
		output.ErrCodeLiveUnavailable,
		command,
		"enable auto-export in the rep+ extension, then retry",
		"or run: rep live-export --once  # one-shot capture from the extension",
		"or load saved data: rep list --saved latest",
	)
	return output.EmitAgentError(os.Stdout, ae, getOutputMode() == "json")
}

// emitLiveEmpty is the shared exit path when live.json exists but has zero
// requests. This is distinct from live-unavailable — capture is working, just
// no traffic yet. Exit 0 is correct here (it is a valid steady state).
func emitLiveEmpty(command string) {
	if getOutputMode() == "json" {
		fmt.Println(`{"source":"live.json","command":"` + command + `","data":[],"suggest":["visit a page in the target browser tab to generate traffic","then retry: rep ` + command + `"]}`)
		return
	}
	fmt.Println("source: live.json (0 requests)")
	fmt.Println("result: no requests captured yet")
	fmt.Println("suggest:")
	fmt.Println("  visit a page in the target browser tab")
	fmt.Println("  then retry: rep " + command)
}
