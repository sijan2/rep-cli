package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/jevdom"
	"github.com/repplus/rep-cli/internal/jevrpc"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
)

func decisionOwner() (string, error) {
	selected, err := scope.Current()
	if err != nil {
		return "", err
	}
	if selected.Scoped {
		return selected.Workspace + "/" + selected.Task, nil
	}
	// Older unscoped browser commands remain usable, without sharing an execution
	// lease with another CLI process. Named tasks retain reuse across invocations.
	return fmt.Sprintf("local/%d", os.Getpid()), nil
}

func hostSelectBatch(ctx context.Context, browser jevdom.Browser, config jev.Config, options []jevdom.Options) ([]jevdom.Result, error) {
	var capabilities jevrpc.Capabilities
	if err := browser.Call(ctx, "jev.capabilities", nil, &capabilities); err != nil || capabilities.Version != jevrpc.Version || !capabilities.PersistentSelection || !capabilities.BatchSelection {
		return nil, output.NewAgentError("host_outdated", "", "the connected rep-host predates this rep CLI and cannot run Jev decisions; install matching binaries and reconnect the browser",
			"scripts/build_install.sh --host  # in the rep-cli checkout", "rep browser reload-extension --browser arc")
	}
	// Credential negotiation is host-local, before any browser RPC. Older hosts
	// receive only the capability query and never receive this credential.
	request := jevrpc.SelectRequest{Version: jevrpc.Version, Options: options}
	if config.APIKey != "" {
		request.Credentials = &jevrpc.Credentials{APIKey: config.APIKey}
	}
	var results []jevdom.Result
	if len(options) == 1 {
		var result jevdom.Result
		if err := browser.Call(ctx, "jev.select", request, &result); err != nil {
			return nil, err
		}
		results = []jevdom.Result{result}
	} else {
		var batch jevrpc.BatchResult
		if err := browser.Call(ctx, "jev.select_batch", request, &batch); err != nil {
			return nil, err
		}
		results = batch.Results
	}
	if len(results) != len(options) {
		return nil, errors.New("host returned an incomplete decision batch")
	}
	return results, nil
}

func runSelections(ctx context.Context, dependencies jevDOMDependencies, browser jevdom.Browser, config jev.Config, options []jevdom.Options) ([]jevdom.Result, error) {
	if dependencies.selectBatch != nil {
		return dependencies.selectBatch(ctx, browser, config, options)
	}
	selector := jevdom.Selector{Browser: browser, Evaluator: dependencies.evaluator(config)}
	for _, option := range options {
		if !option.NoCache && dependencies.cache != nil {
			selector.Cache = dependencies.cache()
			break
		}
	}
	return selector.SelectBatch(ctx, options)
}
