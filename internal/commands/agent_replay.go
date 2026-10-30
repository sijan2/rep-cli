package commands

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/repplus/rep-cli/internal/agentstate"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var coreReplayWrite bool

func newCoreReplayCommand() *cobra.Command {
	var sets []string
	var matrix string
	var write bool
	var timeout time.Duration
	command := &cobra.Command{Use: "replay h_ID", Short: "Replay captured traffic in its browser session; writes need --write", Args: cobra.ExactArgs(1)}
	command.Flags().StringArrayVar(&sets, "set", nil, "Mutation header.NAME, query.NAME, body.PATH, or method=METHOD")
	command.Flags().StringVar(&matrix, "matrix", "", "One variant per value: PATH=[v1,v2,...]")
	command.Flags().BoolVar(&write, "write", false, "Permit sequential unsafe requests; never retry")
	command.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Total replay budget (1s–5m)")
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := coreReplay(cmd, args[0], sets, matrix, write, timeout); err != nil {
			return coreFailure(cmd, err)
		}
		return nil
	}
	return command
}
func coreReplay(command *cobra.Command, handle string, sets []string, matrix string, write bool, timeout time.Duration) error {
	capture, source, err := coreCapturedRequest(handle)
	if err != nil {
		return err
	}
	selected, _ := scope.Current()
	if selected.Identity() != capture.Owner {
		return agentstate.Failure("owned_by_other_task", "explicit task does not own this capture", coreNext("scope"))
	}
	variants, origin, err := coreReplayVariants(source, sets, matrix, write)
	if err != nil {
		return err
	}
	return coreRunTransport(command, variants, origin, source, &capture, timeout, write, "replay")
}
func coreReplayVariants(source *store.Request, sets []string, matrix string, write bool) ([]fanoutVariant, string, error) {
	method, err := coreMethod(source.Method)
	if err != nil {
		return nil, "", err
	}
	originalUnsafe := !coreSafeMethod(method)
	constants := []mutation{}
	for _, spec := range sets {
		if strings.HasPrefix(spec, "method=") {
			method, err = coreMethod(strings.TrimPrefix(spec, "method="))
			if err != nil {
				return nil, "", err
			}
			continue
		}
		mutation, e := parseOneMutation(spec)
		if e != nil {
			return nil, "", coreArgument("mutations use header.NAME, query.NAME, body.PATH, or method=METHOD")
		}
		constants = append(constants, mutation)
	}
	if (originalUnsafe || !coreSafeMethod(method)) && !write {
		return nil, "", agentstate.Failure("write_required", "unsafe replay requires --write", coreNext("replay", "--help"))
	}
	copy := *source
	copy.Method = method
	if originalUnsafe {
		bytes, _, evidence, e := bodyEvidence(source, true)
		if e != nil || (evidence.State != "complete" && evidence.State != "not_applicable") {
			return nil, "", agentstate.Failure("request_body_incomplete", "cannot replay an incompletely captured request body", coreNext("body", source.ID, "--request"))
		}
		copy.Body = string(bytes)
	}
	if _, err = coreRequestURL(source.URL); err != nil {
		return nil, "", err
	}
	origin, err := coreRequestURL(source.URL)
	if err != nil {
		return nil, "", err
	}
	if source.PageURL != "" {
		if pageOrigin, e := coreRequestURL(source.PageURL); e == nil {
			origin = pageOrigin
		}
	}
	values := []string{""}
	key := ""
	if matrix != "" {
		var raw string
		key, raw, _ = strings.Cut(matrix, "=")
		if _, e := parseOneMutation(key + "=x"); e != nil {
			return nil, "", coreArgument("matrix uses header.NAME, query.NAME, or body.PATH")
		}
		var decoded []json.RawMessage
		if json.Unmarshal([]byte(raw), &decoded) == nil {
			values = nil
			for _, value := range decoded {
				var s string
				if json.Unmarshal(value, &s) != nil {
					s = string(value)
				}
				values = append(values, s)
			}
		} else {
			key, values, err = parseMatrixSpec(matrix)
			if err != nil {
				return nil, "", coreArgument("matrix values must be a nonempty [v1,v2,...] list")
			}
		}
		if len(values) < 1 || len(values) > maxFanoutVariants {
			return nil, "", coreArgument("provide between 1 and 256 matrix values")
		}
	}
	variants := make([]fanoutVariant, 0, len(values))
	for i, value := range values {
		mutations := append([]mutation{}, constants...)
		if key != "" {
			m, e := parseOneMutation(key + "=" + value)
			if e != nil {
				return nil, "", e
			}
			mutations = append(mutations, m)
		}
		modified, e := applyMutations(&copy, mutations)
		if e != nil {
			return nil, "", coreArgument("mutation could not be applied to the captured request")
		}
		headers := fanoutForwardHeaders(modified.Headers)
		for name, value := range headers {
			if !coreHTTPToken.MatchString(name) || strings.ContainsAny(value, "\r\n\x00") {
				return nil, "", coreArgument("invalid request header")
			}
		}
		variant := fanoutVariant{Index: i, Method: method, URL: modified.URL, Headers: headers}
		if !coreSafeMethod(method) && modified.Body != "" {
			body := modified.Body
			variant.Body = &body
		}
		variants = append(variants, variant)
	}
	return variants, origin, nil
}

// Retain old request-ID spellings during the staged migration. The final
// constructor above is used when the integrator removes compatibility flags.
func init() {
	legacy := replayCmd.RunE
	replayCmd.Flags().BoolVar(&coreReplayWrite, "write", false, "Permit sequential unsafe requests; never retry")
	replayCmd.RunE = func(cmd *cobra.Command, args []string) error {
		if strings.HasPrefix(args[0], "h_") {
			if len(replayMatrix) > 1 {
				return coreFailure(cmd, coreArgument("provide one matrix"))
			}
			if len(replayAs) > 0 || replayBaseline != 0 || cmd.Flags().Changed("concurrency") {
				return coreFailure(cmd, coreArgument("captured handles use browser credentials and adaptive concurrency"))
			}
			matrix := ""
			if len(replayMatrix) == 1 {
				matrix = replayMatrix[0]
			}
			if err := coreReplay(cmd, args[0], replaySet, matrix, coreReplayWrite, replayTimeout); err != nil {
				return coreFailure(cmd, err)
			}
			return nil
		}
		return legacy(cmd, args)
	}
}
