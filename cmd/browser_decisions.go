package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/jevdom"
	"github.com/repplus/rep-cli/internal/jevrpc"
	"github.com/spf13/cobra"
)

type goalBatch struct {
	Version int `json:"version"`
	Goals   []struct {
		ID   string `json:"id"`
		Goal string `json:"goal"`
	} `json:"goals"`
}

func readGoalBatch(path string) (goalBatch, error) {
	var batch goalBatch
	file, err := os.Open(path)
	if err != nil {
		return batch, errors.New("cannot open goal batch")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 128*1024+1))
	if err != nil || len(data) > 128*1024 {
		return batch, errors.New("goal batch exceeds 128 KiB or cannot be read")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&batch) != nil || decoder.Decode(new(any)) != io.EOF || batch.Version != 1 || len(batch.Goals) == 0 || len(batch.Goals) > jevrpc.MaxBatch {
		return batch, errors.New("provide a version 1 batch with 1 to 32 goals")
	}
	ids := map[string]bool{}
	for _, item := range batch.Goals {
		if item.ID == "" || len(item.ID) > 128 || ids[item.ID] {
			return batch, errors.New("each goal requires a unique id of at most 128 bytes")
		}
		ids[item.ID] = true
	}
	return batch, nil
}

func newBrowserSelectBatchCommand(deps jevDOMDependencies) *cobra.Command {
	options := jevdom.DefaultOptions()
	browserName := "arc"
	command := &cobra.Command{Use: "select-batch goals.json --tab ID", Short: "Resolve independent goals from one observation with shared Jev requests", Args: cobra.ExactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		batch, err := readGoalBatch(args[0])
		if err != nil {
			return reportJevError(command, err)
		}
		if !validDecisionBrowser(browserName) {
			return reportJevError(command, errors.New("unsupported browser"))
		}
		all := make([]jevdom.Options, len(batch.Goals))
		for index, item := range batch.Goals {
			option := options
			option.Goal = item.Goal
			all[index], err = option.Validate()
			if err != nil {
				return reportJevError(command, err)
			}
		}
		config, err := deps.loadConfig()
		if err != nil {
			return reportJevError(command, err)
		}
		owner, err := decisionOwner()
		if err != nil {
			return reportJevError(command, err)
		}
		for index := range all {
			all[index].Model = config.Model
			if deps.selectBatch != nil {
				all[index].Owner = owner
			}
		}
		ctx, cancel := context.WithTimeout(command.Context(), jevdom.Timeout)
		defer cancel()
		browser, err := deps.browser(ctx, browserName)
		if err != nil {
			return reportJevError(command, err)
		}
		results, err := runSelections(ctx, deps, browser, config, all)
		if err != nil {
			return reportJevError(command, err)
		}
		if len(results) != len(all) {
			return reportJevError(command, errors.New("incomplete decision batch"))
		}
		type item struct {
			ID       string        `json:"id"`
			Decision jevdom.Result `json:"decision"`
		}
		output := struct {
			Version int       `json:"version"`
			Results []item    `json:"results"`
			Usage   jev.Usage `json:"usage"`
		}{Version: 1, Results: make([]item, len(results))}
		for index, result := range results {
			output.Results[index] = item{ID: batch.Goals[index].ID, Decision: result}
			output.Usage.InputTokens += result.Usage.InputTokens
			output.Usage.OutputTokens += result.Usage.OutputTokens
		}
		return emitJev(command, command.CommandPath(), output)
	}}
	command.Flags().IntVar(&options.TabID, "tab", -1, "Existing browser tab id")
	command.Flags().StringVar(&browserName, "browser", "arc", "arc, chrome, any, or task-owned headless")
	command.Flags().StringVar(&options.Kind, "kind", "controls", "Candidate roles: controls, text, or all")
	command.Flags().IntVar(&options.Limit, "limit", 240, "Maximum candidates per goal (1 to 480)")
	command.Flags().Float64Var(&options.Confidence, "confidence", 0.8, "Minimum decision confidence")
	command.Flags().BoolVar(&options.NoCache, "no-cache", false, "Bypass decision reuse")
	command.Flags().StringVar(&options.Origin, "origin", "", "Require and restrict to this origin")
	command.Flags().StringVar(&options.FrameID, "frame", "", "Restrict observation to this frame")
	command.Flags().StringVar(&options.FrameURL, "frame-url", "", "Restrict to the unique frame at this exact URL")
	command.Flags().Int64Var(&options.ScopeBackendDOMNodeID, "root-node", 0, "Restrict candidates to this backend node subtree; requires a frame")
	command.Flags().StringVar(&options.Strategy, "strategy", "auto", "auto or legacy")
	command.Flags().StringVar(&options.ObservationMode, "observation", "auto", "auto or legacy")
	_ = command.MarkFlagRequired("tab")
	return command
}

func validDecisionBrowser(name string) bool {
	return name == "arc" || name == "chrome" || name == "any" || name == "headless"
}

func newBrowserObservationCommand(validate bool, deps jevDOMDependencies) *cobra.Command {
	var tab, limit int
	var rootNode int64
	var name, kind, origin, frame, frameURL, fingerprint, generation, since string
	use, description := "observe --tab ID", "Read a compact, relational browser observation without model inference"
	if validate {
		use, description = "validate --tab ID --fingerprint HASH --generation ID", "Refresh evidence and validate an earlier semantic observation"
	}
	command := &cobra.Command{Use: use, Short: description, Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		if tab < 0 || !validDecisionBrowser(name) || (kind != "controls" && kind != "text" && kind != "all") || limit < 1 || limit > 4096 || rootNode < 0 || (rootNode > 0 && frame == "" && frameURL == "") {
			return reportJevError(command, errors.New("invalid tab, browser, kind, candidate limit, or subtree scope"))
		}
		owner, err := decisionOwner()
		if err != nil {
			return reportJevError(command, err)
		}
		ctx, cancel := context.WithTimeout(command.Context(), 30*time.Second)
		defer cancel()
		browser, err := deps.browser(ctx, name)
		if err != nil {
			return reportJevError(command, err)
		}
		params := map[string]any{"tab_id": tab, "owner": owner, "kind": kind, "origin": origin, "frame_id": frame, "frame_url": frameURL, "root_backend_dom_node_id": rootNode}
		method := "browser.observe"
		if since != "" {
			params["since"] = since
		}
		if validate {
			params["fingerprint"], params["generation"] = fingerprint, generation
			method = "browser.validate"
		}
		var result map[string]any
		if err := browser.Call(ctx, method, params, &result); err != nil {
			return reportJevError(command, err)
		}
		projection := result
		if snapshot, ok := result["snapshot"].(map[string]any); ok {
			projection = snapshot
		}
		if candidates, ok := projection["candidates"].([]any); ok {
			if coverage, ok := projection["coverage"].(map[string]any); ok {
				coverage["returned_candidates"] = min(limit, len(candidates))
				coverage["output_truncated"] = len(candidates) > limit
			}
			projection["candidates"] = candidates[:min(limit, len(candidates))]
		}
		if delta, ok := projection["delta"].(map[string]any); ok {
			if upserted, ok := delta["upserted"].([]any); ok {
				truncated := len(upserted) > limit
				delta["total_upserted"], delta["truncated"] = len(upserted), truncated
				delta["can_advance"] = !truncated
				delta["upserted"] = upserted[:min(limit, len(upserted))]
				if coverage, ok := projection["coverage"].(map[string]any); ok {
					coverage["returned_candidates"] = min(limit, len(upserted))
					coverage["output_truncated"] = truncated
				}
			}
		}
		return emitJev(command, command.CommandPath(), result)
	}}
	command.Flags().IntVar(&tab, "tab", -1, "Existing task-owned tab")
	command.Flags().StringVar(&name, "browser", "arc", "arc, chrome, any, or task-owned headless")
	command.Flags().StringVar(&kind, "kind", "controls", "controls, text, or all")
	command.Flags().StringVar(&origin, "origin", "", "Require and restrict to this origin")
	command.Flags().StringVar(&frame, "frame", "", "Restrict observation to this frame")
	command.Flags().StringVar(&frameURL, "frame-url", "", "Restrict to the unique frame at this exact URL")
	command.Flags().Int64Var(&rootNode, "root-node", 0, "Restrict candidates to this backend node subtree; requires a frame")
	if !validate {
		command.Flags().StringVar(&since, "since", "", "Return changes from a retained observation fingerprint")
	}
	command.Flags().IntVar(&limit, "limit", 64, "Maximum candidates returned (1 to 4096); full fingerprint retained")
	_ = command.MarkFlagRequired("tab")
	if validate {
		command.Flags().StringVar(&fingerprint, "fingerprint", "", "Prior full semantic fingerprint")
		command.Flags().StringVar(&generation, "generation", "", "Prior browser session generation")
		_ = command.MarkFlagRequired("fingerprint")
		_ = command.MarkFlagRequired("generation")
	}
	return command
}

func newBrowserRuntimeCommand(deps jevDOMDependencies) *cobra.Command {
	name := "arc"
	command := &cobra.Command{Use: "runtime", Short: "Inspect persistent decision concurrency and reuse counters", Args: cobra.NoArgs, RunE: func(command *cobra.Command, _ []string) error {
		if !validDecisionBrowser(name) {
			return reportJevError(command, errors.New("unsupported browser"))
		}
		ctx, cancel := context.WithTimeout(command.Context(), 5*time.Second)
		defer cancel()
		browser, err := deps.browser(ctx, name)
		if err != nil {
			return reportJevError(command, err)
		}
		var status map[string]any
		if err := browser.Call(ctx, "jev.runtime", nil, &status); err != nil {
			return reportJevError(command, err)
		}
		return emitJev(command, command.CommandPath(), status)
	}}
	command.Flags().StringVar(&name, "browser", "arc", "arc, chrome, any, or task-owned headless")
	return command
}

func init() {
	deps := defaultJevDOMDependencies()
	browserCmd.AddCommand(newBrowserSelectBatchCommand(deps), newBrowserObservationCommand(false, deps), newBrowserObservationCommand(true, deps), newBrowserRuntimeCommand(deps))
}
