package cmd

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/probe"
	"github.com/repplus/rep-cli/internal/scope"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
)

var probeCmd = probeNewCommand()

type probeAddResult struct {
	Probe   probe.Probe `json:"probe"`
	Created bool        `json:"created"`
}

func init() {
	rootCmd.AddCommand(probeCmd)
}

func probeNewCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "probe", Short: "Track deduplicated experiments and the remaining research frontier",
		Long: `Plan task-scoped experiments without executing requests. Content-addressed
probes retain immutable proposal and verdict records. next is a bounded view:
it never dequeues work, and recorded probes never return to the frontier.

record stores caller adjudication as model_assessed, not executed_and_observed.
A pass supports the hypothesis; a fail refutes it; inconclusive stays unknown.
An empty frontier completes the recorded plan, not a proof of target security.

Oracles: authz_cross_principal, authz_unauthenticated, object_reference,
input_reflection, mutation_side_effect, rate_or_lockout, information_disclosure.`,
	}
	var hypothesis, oracle, ref, precondition, mutationShape string
	var principals []string
	add := &cobra.Command{Use: "add --hypothesis H --oracle KIND --ref REQUEST_ID", Short: "Add one experiment, or return its existing record", Args: probeNoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(hypothesis) == "" || strings.TrimSpace(ref) == "" || oracle == "" {
				return probeError(cmd, output.ErrCodeInvalidArgument, "--hypothesis, --oracle, and --ref are required")
			}
			kind, err := probe.ProbeParseOracle(oracle)
			if err != nil {
				return probeStoreError(cmd, err, false)
			}
			ledger, err := probeOpenStore(cmd)
			if err != nil {
				return err
			}
			req, err := findBodyWebRequest(ref, "")
			if err != nil {
				return probeError(cmd, output.ErrCodeStoreRead, err.Error())
			}
			if req == nil {
				return probeError(cmd, output.ErrCodeRequestNotFound, fmt.Sprintf("no captured request matched %q in this task", ref), probeCommandLine("list", "--limit", "20"))
			}
			p, created, err := ledger.ProbeAdd(probe.ProbeSpec{
				Method: req.Method, URL: req.URL, RequestRef: req.ID, Hypothesis: hypothesis,
				OracleKind: kind, Precondition: precondition, MutationShape: mutationShape, IdentityShape: principals,
			})
			if err != nil {
				return probeStoreError(cmd, err, true)
			}
			return probeEmit(cmd, probeAddResult{Probe: p, Created: created}, nil, 0, 0, probeSuggestions(p))
		}}
	add.Flags().StringVar(&hypothesis, "hypothesis", "", "One sentence describing the hypothesis and why it matters")
	add.Flags().StringVar(&oracle, "oracle", "", "Fixed oracle kind (see rep probe --help and rep probe coverage)")
	add.Flags().StringVar(&ref, "ref", "", "Captured request ID or unambiguous alias in this task")
	add.Flags().StringVar(&precondition, "precondition", "", "Prerequisite for an interpretable result (default: oracle-specific)")
	add.Flags().StringVar(&mutationShape, "mutation-shape", "", "Stable mutation descriptor; defaults to the oracle's standard experiment")
	advancedFlags(add, "precondition", "mutation-shape")
	add.Flags().StringSliceVar(&principals, "as", nil, "Principal names that distinguish this experiment; order and duplicates are ignored")
	command.AddCommand(add)

	var nextLimit int
	var nextOracle string
	next := &cobra.Command{Use: "next", Short: "View untested probes, highest priority first; does not dequeue", Args: probeNoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ledger, err := probeOpenStore(cmd)
			if err != nil {
				return err
			}
			batch, err := ledger.ProbeNext(nextLimit, probe.ProbeOracleKind(nextOracle))
			if err != nil {
				return probeStoreError(cmd, err, false)
			}
			suggestions := []string{probeCommandLine("probe", "coverage")}
			if len(batch.Probes) > 0 {
				suggestions = probeSuggestions(batch.Probes[0])
			} else if !batch.FrontierEmpty {
				suggestions = []string{probeCommandLine("probe", "next")}
			}
			return probeEmit(cmd, batch, map[string]interface{}{"limit": nextLimit, "oracle": nextOracle}, len(batch.Probes), batch.MatchingUntested, suggestions)
		}}
	next.Flags().IntVar(&nextLimit, "limit", probe.ProbeDefaultLimit, "Maximum probes to return (1-100)")
	next.Flags().StringVar(&nextOracle, "oracle", "", "Only this oracle kind")
	command.AddCommand(next)

	var verdict, state, note string
	var evidence []string
	record := &cobra.Command{Use: "record ID --verdict pass|fail|inconclusive --evidence REF", Short: "Append a verdict or an attempted/blocked prerequisite record", Args: probeOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			ledger, err := probeOpenStore(cmd)
			if err != nil {
				return err
			}
			p, err := ledger.ProbeRecord(args[0], probe.ProbeRecordSpec{Verdict: probe.ProbeVerdict(verdict), State: probe.ProbeState(state), Evidence: evidence, Note: note})
			if err != nil {
				return probeStoreError(cmd, err, true)
			}
			return probeEmit(cmd, p, nil, 0, 0, probeSuggestions(p))
		}}
	record.Flags().StringVar(&verdict, "verdict", "", "pass supports the hypothesis; fail refutes it; inconclusive remains unknown")
	record.Flags().StringVar(&state, "state", "", "Instead of a verdict: attempted or blocked (blocked requires --note)")
	record.Flags().StringArrayVar(&evidence, "evidence", nil, "Run/operation ID or run_ID/op_ID pointer; repeat for multiple references")
	record.Flags().StringVar(&note, "note", "", "Adjudication reason or failed precondition, never an inline response body")
	command.AddCommand(record)

	var coverageLimit int
	coverage := &cobra.Command{Use: "coverage", Short: "Count every state and oracle, and list what remains untested", Args: probeNoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ledger, err := probeOpenStore(cmd)
			if err != nil {
				return err
			}
			result, err := ledger.ProbeCoverage(coverageLimit)
			if err != nil {
				return probeStoreError(cmd, err, false)
			}
			suggestions := []string{}
			if !result.FrontierEmpty {
				suggestions = append(suggestions, probeCommandLine("probe", "next"))
			}
			return probeEmit(cmd, result, map[string]interface{}{"limit": coverageLimit}, len(result.Untested), result.ByState[probe.ProbeUntested], suggestions)
		}}
	coverage.Flags().IntVar(&coverageLimit, "limit", 20, "Maximum untested probes to list (1-100); counts always cover the entire ledger")
	command.AddCommand(coverage)

	var showLimit int
	show := &cobra.Command{Use: "show ID", Short: "Show a probe and its latest immutable history entries", Args: probeOneArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			ledger, err := probeOpenStore(cmd)
			if err != nil {
				return err
			}
			result, err := ledger.ProbeShow(args[0], showLimit)
			if err != nil {
				return probeStoreError(cmd, err, false)
			}
			return probeEmit(cmd, result, map[string]interface{}{"id": args[0], "limit": showLimit}, len(result.History), result.TotalEvents, probeSuggestions(result.Probe))
		}}
	show.Flags().IntVar(&showLimit, "limit", 20, "Maximum recent history entries (1-100); older records remain in probes.jsonl")
	command.AddCommand(show)
	return command
}

func probeNoArgs(cmd *cobra.Command, args []string) error {
	if len(args) != 0 {
		return probeError(cmd, output.ErrCodeInvalidArgument, "this command accepts no positional arguments")
	}
	return nil
}

func probeOneArg(cmd *cobra.Command, args []string) error {
	if len(args) != 1 {
		return probeError(cmd, output.ErrCodeInvalidArgument, "exactly one probe ID is required")
	}
	return nil
}

func probeOpenStore(cmd *cobra.Command) (*probe.ProbeStore, error) {
	selected, err := scope.Current()
	if err != nil {
		return nil, probeError(cmd, output.ErrCodeInvalidArgument, err.Error())
	}
	if !selected.Scoped || selected.TaskSource == "default" {
		return nil, probeError(cmd, "scope_required", "probe requires an explicit workspace and task; select --workspace and --task or REP_WORKSPACE and REP_TASK", "rep scope")
	}
	path, err := store.GetStorePath()
	if err != nil {
		return nil, probeError(cmd, output.ErrCodeStoreRead, err.Error())
	}
	ledger, err := probe.ProbeOpen(path)
	if err != nil {
		return nil, probeStoreError(cmd, err, false)
	}
	return ledger, nil
}

func probeError(cmd *cobra.Command, code, message string, suggestions ...string) error {
	name := "probe " + cmd.Name()
	return output.EmitAgentError(cmd.OutOrStdout(), output.NewAgentError(code, name, message, suggestions...), getOutputMode() == "json")
}

func probeStoreError(cmd *cobra.Command, err error, write bool) error {
	code := output.ErrCodeStoreRead
	if write {
		code = output.ErrCodeStoreWrite
	}
	switch {
	case errors.Is(err, probe.ProbeErrInvalid):
		code = output.ErrCodeInvalidArgument
	case errors.Is(err, probe.ProbeErrNotFound):
		code = "probe_not_found"
	case errors.Is(err, probe.ProbeErrCorrupt):
		code = output.ErrCodeStoreRead
	}
	return probeError(cmd, code, err.Error())
}

func probeEmit(cmd *cobra.Command, data interface{}, filters map[string]interface{}, returned, total int, suggestions []string) error {
	env := output.WrapData("probe "+cmd.Name(), probe.ProbeLedgerName, data)
	if filters == nil {
		filters = make(map[string]interface{})
	}
	if selected, err := scope.Current(); err == nil {
		filters["workspace"], filters["task"] = selected.Workspace, selected.Task
	}
	env.Filters, env.Suggest = filters, suggestions
	if total > returned {
		env.Truncation = &output.TruncationInfo{Reason: "limit", Returned: returned, Total: total}
	}
	if getOutputMode() == "json" {
		buf, err := sonic.ConfigStd.MarshalIndent(env, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(buf))
		return err
	}
	var text strings.Builder
	switch result := data.(type) {
	case probeAddResult:
		if result.Created {
			fmt.Fprintln(&text, "Added experiment.")
		} else {
			fmt.Fprintln(&text, "Experiment already exists; its original proposal and current state are preserved.")
		}
		probePrintRow(&text, result.Probe)
	case probe.Probe:
		probePrintRow(&text, result)
		fmt.Fprintf(&text, "Assessment: %s (caller adjudication; no oracle executed by probe)\n", result.Assessment)
	case probe.ProbeBatch:
		fmt.Fprintf(&text, "Untested: %d matching, %d across the task\n", result.MatchingUntested, result.TotalUntested)
		for _, p := range result.Probes {
			probePrintRow(&text, p)
		}
		fmt.Fprintln(&text, result.Interpretation)
	case probe.ProbeCoverage:
		fmt.Fprintf(&text, "Planned probes: %d\n", result.Total)
		for _, state := range probe.ProbeStates() {
			fmt.Fprintf(&text, "%s: %d\n", state, result.ByState[state])
		}
		fmt.Fprintln(&text, "Oracle counts (untested / attempted / inconclusive / verified / refuted / blocked):")
		for _, kind := range probe.ProbeOracleKinds() {
			counts := result.ByOracle[kind]
			fmt.Fprintf(&text, "  %s: %d total", kind, counts.Total)
			for _, state := range probe.ProbeStates() {
				fmt.Fprintf(&text, " / %d", counts.ByState[state])
			}
			fmt.Fprintln(&text)
		}
		for _, p := range result.Untested {
			probePrintRow(&text, p)
		}
		fmt.Fprintln(&text, result.Interpretation)
	case probe.ProbeDetail:
		probePrintRow(&text, result.Probe)
		fmt.Fprintf(&text, "Mutation shape: %s\nIdentity shape: %s\n", result.Probe.MutationShape, strings.Join(result.Probe.IdentityShape, ","))
		for _, event := range result.History {
			fmt.Fprintf(&text, "  #%d %s %s verdict=%s evidence=%s assessment=%s\n", event.Sequence,
				event.Kind, event.State, event.Verdict, strings.Join(event.Evidence, ","), event.Assessment)
			if event.Note != "" {
				fmt.Fprintf(&text, "    %s\n", event.Note)
			}
		}
	}
	if total > returned {
		fmt.Fprintf(&text, "Omitted: %d (limit; retained in %s)\n", total-returned, probe.ProbeLedgerName)
	}
	for _, suggestion := range suggestions {
		fmt.Fprintf(&text, "Suggest: %s\n", suggestion)
	}
	_, err := io.WriteString(cmd.OutOrStdout(), text.String())
	return err
}

func probePrintRow(w io.Writer, p probe.Probe) {
	fmt.Fprintf(w, "%s [%s] oracle=%s priority=%d ref=%s\n  %s\n  Hypothesis: %s\n  Precondition: %s\n",
		p.ID, p.State, p.OracleKind, p.Priority, p.RequestRef, p.EndpointFamily, p.Hypothesis, p.Precondition)
	if len(p.Evidence) > 0 {
		fmt.Fprintf(w, "  Evidence: %s\n", strings.Join(p.Evidence, ", "))
	}
	if p.Note != "" {
		fmt.Fprintf(w, "  Note: %s\n", p.Note)
	}
}

func probeSuggestions(p probe.Probe) []string {
	if p.State != probe.ProbeUntested {
		return []string{probeCommandLine("probe", "show", p.ID), probeCommandLine("probe", "next")}
	}
	replay := []string{"replay", p.RequestRef, "--baseline", "5"}
	principals := append([]string{}, p.IdentityShape...)
	if len(principals) == 0 {
		switch p.OracleKind {
		case probe.ProbeAuthzUnauthenticated:
			principals = []string{"anon"}
		case probe.ProbeAuthzCrossPrincipal, probe.ProbeObjectReference:
			identities, err := loadIdentities()
			if err != nil {
				return []string{probeCommandLine("identity", "list"), probeCommandLine(replay...)}
			}
			fingerprints := make(map[string]bool)
			names := make([]string, 0, len(identities))
			for name := range identities {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				identity := identities[name]
				if identity.Fingerprint != "" && !fingerprints[identity.Fingerprint] && !strings.ContainsAny(name, ",\r\n") && name != "anon" {
					principals = append(principals, name)
					fingerprints[identity.Fingerprint] = true
				}
				if len(principals) == 2 {
					break
				}
			}
			if len(principals) < 2 {
				return []string{probeCommandLine("identity", "list"), probeCommandLine(replay...)}
			}
		}
	} else {
		identities, err := loadIdentities()
		for _, name := range principals {
			if _, bound := identities[name]; name != "anon" && (!bound || err != nil) {
				return []string{probeCommandLine("identity", "list"), probeCommandLine(replay...)}
			}
		}
	}
	if len(principals) > 0 {
		replay = append(replay, "--as", strings.Join(principals, ","))
	}
	return []string{probeCommandLine(replay...), probeCommandLine("probe", "show", p.ID)}
}

// Suggestions must survive a shell round trip without turning captured or
// user-chosen labels into shell syntax, and must retain the task selection.
func probeCommandLine(args ...string) string {
	parts := []string{"rep"}
	if selected, err := scope.Current(); err == nil && selected.Scoped {
		parts = append(parts, "--workspace", selected.Workspace, "--task", selected.Task)
	}
	parts = append(parts, args...)
	for i, part := range parts {
		if part == "" || strings.IndexFunc(part, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_./,:=-", r))
		}) >= 0 {
			parts[i] = "'" + strings.ReplaceAll(part, "'", "'\"'\"'") + "'"
		}
	}
	return strings.Join(parts, " ")
}
