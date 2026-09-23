package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/repplus/rep-cli/internal/contextview"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

type trafficSummaryOptions struct {
	Saved    string
	Domain   string
	Primary  bool
	MaxBytes int
	Since    string
}

func newTrafficSummaryCommand(name string) *cobra.Command {
	options := trafficSummaryOptions{}
	command := &cobra.Command{
		Use:   name,
		Short: "Bounded, task-local traffic overview with change-only cursors",
		Long: `Read the selected task's latest capture, or one explicitly selected archive.
Never combines unrelated saved sessions or persistent notes. Groups repeated
endpoints, omits bodies/headers/query values, and enforces an output byte budget.

Use --since CURSOR from a previous result to receive only changed or previously
omitted groups. A cursor belongs to this scope and source/filter selection.
Empty tasks return no_capture, without falling back to shared browser traffic.
Output is compact JSON in every output mode; --envelope is optional.

Examples:
  rep --workspace shopping --task ebay summary --max-bytes 4096
  rep --workspace applications --task form context --since CURSOR
  rep --workspace applications --task form summary --saved SESSION_ID`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := buildTrafficSummary(options, forceEnvelope && !rawJSON, cmd.Name())
			if err != nil {
				return output.EmitAgentError(cmd.OutOrStdout(), output.NewAgentError(
					"context_unavailable", cmd.Name(), err.Error(), "rep scope", "rep describe summary",
				), true)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return err
		},
	}
	command.Flags().StringVar(&options.Saved, "saved", "", "Read one archive (exact ID, unique prefix, or latest)")
	command.Flags().StringVarP(&options.Domain, "domain", "d", "", "Include this exact hostname and its subdomains")
	command.Flags().BoolVar(&options.Primary, "primary", false, "Include only this task's primary domains")
	command.Flags().IntVar(&options.MaxBytes, "max-bytes", 8192, "Maximum output bytes including newline (2048-65536)")
	command.Flags().StringVar(&options.Since, "since", "", "Return changes after an acknowledged summary cursor")
	return command
}

func buildTrafficSummary(options trafficSummaryOptions, envelope bool, command string) ([]byte, error) {
	if options.MaxBytes < 2048 || options.MaxBytes > 65536 {
		return nil, errors.New("max-bytes must be between 2048 and 65536")
	}
	selected, err := scope.Current()
	if err != nil {
		return nil, err
	}
	input, err := loadTrafficSummary(selected, options)
	if err != nil {
		return nil, err
	}
	budget := options.MaxBytes
	if envelope {
		empty, _ := json.Marshal(output.WrapData(command, input.Source, json.RawMessage(`{}`)))
		budget -= len(empty) - 2
	}
	data, err := contextview.Build(input, contextview.Options{
		Budget: budget, Since: options.Since,
		CacheDir: filepath.Join(selected.DataDir, "context-checkpoints"),
	})
	if err != nil {
		return nil, err
	}
	if envelope {
		data, err = json.Marshal(output.WrapData(command, input.Source, json.RawMessage(data)))
	}
	return data, err
}

func loadTrafficSummary(selected scope.Scope, options trafficSummaryOptions) (contextview.Input, error) {
	input := contextview.Input{
		ScopeID: selected.DataDir,
		Source:  "live",
		Provenance: contextview.Provenance{
			Workspace: selected.Workspace, Task: selected.Task,
			SourceStatus: "no_capture", LegacyGlobal: !selected.Scoped,
		},
	}
	domain := strings.ToLower(strings.TrimSpace(options.Domain))
	if domain != "" && !validSummaryDomain(domain) {
		return input, errors.New("domain must be a hostname without a scheme, port, path, or wildcard")
	}
	var persistent *store.Store
	var err error
	if options.Saved != "" || options.Primary {
		// Load directly rather than reusing a singleton from another command.
		persistent, err = store.Load()
		if err != nil {
			return input, errors.New("cannot read the selected task's saved metadata")
		}
	}
	var export store.Export
	if options.Saved != "" {
		var session *store.Session
		if options.Saved == "latest" || options.Saved == "last" {
			session = persistent.GetLatestSession()
		} else {
			session, err = selectSummaryArchive(persistent.Sessions, options.Saved)
			if err != nil {
				return input, err
			}
		}
		if session == nil {
			return input, errors.New("saved session not found in this task")
		}
		export.Requests = session.Requests
		export.SessionID = session.CaptureSessionID
		input.Source = "saved"
		archiveIdentity, _ := json.Marshal([]any{session.ID, session.HashID, session.Timestamp, session.CaptureSessionID, session.CaptureDigest})
		input.SourceID = string(archiveIdentity)
		input.Sessions = 1
		input.Provenance.SourceStatus = "saved"
	} else {
		livePath, err := store.GetLiveFilePath()
		if err != nil {
			return input, err
		}
		export, err = loadLiveExport(livePath)
		if err != nil && !os.IsNotExist(err) {
			return input, errors.New("the selected task's live capture is unreadable; no archive or global fallback was used")
		}
		if err == nil {
			input.Provenance.SourceStatus = "captured"
		}
		input.SourceID = livePath
	}
	input.Provenance.CaptureSessionID = export.SessionID
	input.Provenance.ExportedAt = export.ExportedAt
	primaries := []string{}
	if options.Primary {
		primaries = persistent.GetPrimaryDomains()
		sort.Strings(primaries)
	}
	for _, request := range export.Requests {
		parsed, err := url.Parse(request.URL)
		if err != nil {
			input.ExcludedRequests++
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		include := domain == "" || summaryHostMatches(host, domain)
		if include && options.Primary {
			include = false
			for _, primary := range primaries {
				if summaryHostMatches(host, strings.ToLower(primary)) {
					include = true
					break
				}
			}
		}
		if include {
			input.Requests = append(input.Requests, request)
		} else {
			input.ExcludedRequests++
		}
	}
	// Live identity remains stable across captures so a cursor can describe
	// changes. Switching archives or filters requires a new baseline.
	identity, _ := json.Marshal([]any{selected.DataDir, input.Source, input.SourceID, domain, options.Primary, primaries})
	input.SourceID = string(identity)
	return input, nil
}

func summaryHostMatches(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func validSummaryDomain(domain string) bool {
	if len(domain) > 253 {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-') {
				return false
			}
		}
	}
	return true
}

// Match full hashes first, full IDs second, then a unique prefix. Timestamp IDs
// can collide; selecting an arbitrary first archive would hide source changes.
func selectSummaryArchive(sessions []store.Session, selector string) (*store.Session, error) {
	for _, byHash := range []bool{true, false} {
		var exact *store.Session
		for index := range sessions {
			candidate := &sessions[index]
			value := candidate.ID
			if byHash {
				value = candidate.HashID
			}
			if value != selector {
				continue
			}
			if exact != nil {
				return nil, errors.New("saved session ID is ambiguous; use its full hash ID")
			}
			exact = candidate
		}
		if exact != nil {
			return exact, nil
		}
	}
	var match *store.Session
	for index := range sessions {
		candidate := &sessions[index]
		if strings.HasPrefix(candidate.ID, selector) || strings.HasPrefix(candidate.HashID, selector) {
			if match != nil {
				return nil, errors.New("saved session prefix is ambiguous; use its full hash ID")
			}
			match = candidate
		}
	}
	return match, nil
}
