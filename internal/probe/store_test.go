package probe

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/bytedance/sonic"
)

func probeTestStore(t *testing.T) *ProbeStore {
	t.Helper()
	s, err := ProbeOpen(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func probeTestAdd(t *testing.T, s *ProbeStore, spec ProbeSpec) Probe {
	t.Helper()
	p, created, err := s.ProbeAdd(spec)
	if err != nil || !created {
		t.Fatalf("add: created=%v, error=%v", created, err)
	}
	return p
}

func probeTestBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestProbeJournalRetainsAllOccurrencesAndNeverRequeues(t *testing.T) {
	for _, verdict := range []ProbeVerdict{ProbePass, ProbeFail, ProbeVerdictInconclusive} {
		t.Run(string(verdict), func(t *testing.T) {
			s := probeTestStore(t)
			spec := probeTestSpec()
			original := probeTestAdd(t, s, spec)
			before := probeTestBytes(t, s.path)
			recorded, err := s.ProbeRecord(original.ID, ProbeRecordSpec{Verdict: verdict, Evidence: []string{"run_one/op_one"}, Note: "Oracle adjudication."})
			if err != nil {
				t.Fatal(err)
			}
			after := probeTestBytes(t, s.path)
			if !bytes.HasPrefix(after, before) || bytes.Count(after, []byte{'\n'}) != 2 {
				t.Fatal("record rewrote the proposal or did not append exactly one line")
			}
			if recorded.Assessment != ProbeModelAssessed || len(recorded.Evidence) != 1 {
				t.Fatalf("bad assessment provenance: %+v", recorded)
			}
			spec.Hypothesis = "Reworded hypothesis must not bypass deduplication."
			spec.URL = "https://example.test/users/999?view=full&owner=72"
			spec.RequestRef = "h_new_capture"
			duplicate, created, err := s.ProbeAdd(spec)
			if err != nil || created || !reflect.DeepEqual(duplicate, recorded) || !bytes.Equal(after, probeTestBytes(t, s.path)) {
				t.Fatalf("duplicate changed the record: created=%v, err=%v, probe=%+v", created, err, duplicate)
			}
			batch, err := s.ProbeNext(5, "")
			if err != nil || !batch.FrontierEmpty || len(batch.Probes) != 0 || !strings.Contains(batch.Interpretation, "not proof") {
				t.Fatalf("recorded probe returned to frontier: %+v, %v", batch, err)
			}
			// A later assessment changes only the projection, not the earlier verdict.
			if _, err := s.ProbeRecord(original.ID, ProbeRecordSpec{Verdict: ProbeVerdictInconclusive, Evidence: []string{"op_later"}}); err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(probeTestBytes(t, s.path), after) {
				t.Fatal("earlier verdict bytes changed")
			}
			reopened, err := ProbeOpen(filepath.Dir(s.path))
			if err != nil {
				t.Fatal(err)
			}
			detail, err := reopened.ProbeShow(original.ID, 2)
			if err != nil || detail.TotalEvents != 3 || detail.OmittedEvents != 1 || len(detail.History) != 2 ||
				detail.History[0].Verdict != verdict || detail.Probe.State != ProbeInconclusive || len(detail.Probe.Evidence) != 2 {
				t.Fatalf("incorrect history: %+v, %v", detail, err)
			}
			info, err := os.Stat(s.path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("ledger permissions: %v, %v", info, err)
			}
			staging, err := filepath.Glob(filepath.Join(filepath.Dir(s.path), ".probes-*.tmp"))
			if err != nil || len(staging) != 0 {
				t.Fatalf("staging files left behind: %v, %v", staging, err)
			}
		})
	}
}

func TestProbeNextIsBoundedStableAndPrioritized(t *testing.T) {
	s := probeTestStore(t)
	for _, oracle := range []ProbeOracleKind{ProbeInputReflection, ProbeRateOrLockout, ProbeAuthzCrossPrincipal, ProbeObjectReference, ProbeAuthzUnauthenticated} {
		spec := probeTestSpec()
		spec.OracleKind = oracle
		probeTestAdd(t, s, spec)
	}
	before := probeTestBytes(t, s.path)
	first, err := s.ProbeNext(2, "")
	if err != nil || len(first.Probes) != 2 || first.MatchingUntested != 5 || first.Omitted != 3 ||
		first.Probes[0].OracleKind != ProbeAuthzCrossPrincipal || first.Probes[1].OracleKind != ProbeObjectReference {
		t.Fatalf("wrong priority or bound: %+v, %v", first, err)
	}
	second, err := s.ProbeNext(2, "")
	if err != nil || !reflect.DeepEqual(first, second) || !bytes.Equal(before, probeTestBytes(t, s.path)) {
		t.Fatal("next changed the frontier without a verdict")
	}
	filtered, err := s.ProbeNext(2, ProbeInputReflection)
	if err != nil || len(filtered.Probes) != 1 || filtered.MatchingUntested != 1 || filtered.TotalUntested != 5 || filtered.FrontierEmpty {
		t.Fatalf("incorrect filter: %+v, %v", filtered, err)
	}
	empty, err := s.ProbeNext(2, ProbeMutationSideEffect)
	if err != nil || empty.FrontierEmpty || empty.MatchingUntested != 0 || !strings.Contains(empty.Interpretation, "Other planned") {
		t.Fatalf("filtered empty result falsely claimed completion: %+v, %v", empty, err)
	}
	for _, limit := range []int{-1, 0, ProbeMaxLimit + 1} {
		if _, err := s.ProbeNext(limit, ""); !errors.Is(err, ProbeErrInvalid) {
			t.Fatalf("accepted limit %d: %v", limit, err)
		}
	}
	if _, err := s.ProbeNext(1, "http_200"); !errors.Is(err, ProbeErrInvalid) {
		t.Fatalf("accepted invented oracle: %v", err)
	}
}

func TestProbeCoverageSeparatesEveryStateAndOracle(t *testing.T) {
	s := probeTestStore(t)
	for _, tc := range []struct {
		state   ProbeState
		verdict ProbeVerdict
	}{
		{ProbeUntested, ""}, {ProbeAttempted, ""}, {ProbeInconclusive, ProbeVerdictInconclusive},
		{ProbeVerified, ProbePass}, {ProbeRefuted, ProbeFail}, {ProbeBlocked, ""},
	} {
		spec := probeTestSpec()
		spec.MutationShape = string(tc.state)
		p := probeTestAdd(t, s, spec)
		if tc.state != ProbeUntested {
			record := ProbeRecordSpec{Verdict: tc.verdict, Evidence: []string{"run_one"}, Note: "Prerequisite unavailable."}
			if tc.verdict == "" {
				record.State = tc.state
			}
			if _, err := s.ProbeRecord(p.ID, record); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, kind := range ProbeOracleKinds()[1:] {
		spec := probeTestSpec()
		spec.OracleKind = kind
		probeTestAdd(t, s, spec)
	}
	coverage, err := s.ProbeCoverage(2)
	if err != nil || coverage.Total != 12 || len(coverage.Untested) != 2 || coverage.Omitted != 5 || coverage.FrontierEmpty {
		t.Fatalf("bad coverage: %+v, %v", coverage, err)
	}
	for _, state := range ProbeStates() {
		want := 1
		if state == ProbeUntested {
			want = 7
		}
		if coverage.ByState[state] != want || coverage.ByOracle[ProbeAuthzCrossPrincipal].ByState[state] != 1 {
			t.Fatalf("state %s collapsed: %+v", state, coverage)
		}
	}
	for _, kind := range ProbeOracleKinds()[1:] {
		if coverage.ByOracle[kind].Total != 1 || coverage.ByOracle[kind].ByState[ProbeRefuted] != 0 {
			t.Fatalf("wrong oracle counts for %s", kind)
		}
	}
}

func TestProbeEmptyReadsDoNotWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "untouched")
	s, err := ProbeOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := s.ProbeNext(5, "")
	if err != nil || !batch.FrontierEmpty || batch.Probes == nil || !strings.Contains(batch.Interpretation, "No probes have been planned") {
		t.Fatalf("empty frontier: %+v, %v", batch, err)
	}
	coverage, err := s.ProbeCoverage(5)
	if err != nil || coverage.Total != 0 || len(coverage.ByState) != 6 || len(coverage.ByOracle) != 7 || coverage.Untested == nil {
		t.Fatalf("empty coverage: %+v, %v", coverage, err)
	}
	if _, err := s.ProbeShow("missing", 5); !errors.Is(err, ProbeErrNotFound) {
		t.Fatalf("show missing: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only view created store: %v", err)
	}
}

func TestProbeCorruptHistoryCannotReopenFrontierOrBeOverwritten(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func([]byte) []byte
	}{
		{"partial line", func(data []byte) []byte { return data[:len(data)-1] }},
		{"invalid JSON", func(data []byte) []byte { return append(data, []byte("{bad}\n")...) }},
		{"oversized record", func(data []byte) []byte {
			return append(data, []byte(strings.Repeat("x", probeMaxRecordBytes)+"\n")...)
		}},
		{"unknown oracle", func(data []byte) []byte {
			return bytes.ReplaceAll(data, []byte("authz_cross_principal"), []byte("status_200"))
		}},
		{"duplicate proposal", func(data []byte) []byte {
			var event ProbeEvent
			_ = sonic.Unmarshal(data, &event)
			event.Sequence = 2
			line, _ := sonic.Marshal(event)
			return append(data, append(line, '\n')...)
		}},
		{"changed content identity", func(data []byte) []byte {
			return bytes.ReplaceAll(data, []byte("cross_principal\""), []byte("changed_mutation\""))
		}},
		{"unknown event", func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"kind":"add"`), []byte(`"kind":"forget"`), 1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := probeTestStore(t)
			p := probeTestAdd(t, s, probeTestSpec())
			corrupt := tc.edit(probeTestBytes(t, s.path))
			if err := os.WriteFile(s.path, corrupt, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ProbeNext(5, ""); !errors.Is(err, ProbeErrCorrupt) {
				t.Fatalf("corrupt journal reported as frontier: %v", err)
			}
			if _, _, err := s.ProbeAdd(probeTestSpec()); !errors.Is(err, ProbeErrCorrupt) {
				t.Fatalf("corrupt journal accepted add: %v", err)
			}
			if _, err := s.ProbeRecord(p.ID, ProbeRecordSpec{Verdict: ProbePass, Evidence: []string{"run_one"}}); !errors.Is(err, ProbeErrCorrupt) {
				t.Fatalf("corrupt journal accepted record: %v", err)
			}
			if !bytes.Equal(corrupt, probeTestBytes(t, s.path)) {
				t.Fatal("failed operation rewrote raw history")
			}
		})
	}
}

func TestProbeConcurrentWritersDoNotLoseOrDuplicateRecords(t *testing.T) {
	s := probeTestStore(t)
	const workers = 12
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			writer, err := ProbeOpen(filepath.Dir(s.path))
			if err != nil {
				errs <- err
				return
			}
			p, _, err := writer.ProbeAdd(probeTestSpec())
			if err == nil {
				_, err = writer.ProbeRecord(p.ID, ProbeRecordSpec{Verdict: ProbeVerdictInconclusive, Evidence: []string{fmt.Sprintf("op_%d", i)}})
			}
			if err == nil {
				_, err = writer.ProbeCoverage(1)
			}
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	p, err := ProbeNew(probeTestSpec())
	if err != nil {
		t.Fatal(err)
	}
	detail, err := s.ProbeShow(p.ID, 100)
	if err != nil || detail.TotalEvents != workers+1 || len(detail.Probe.Evidence) != workers {
		t.Fatalf("lost append or duplicate proposal: %+v, %v", detail, err)
	}
	if lines := bytes.Count(probeTestBytes(t, s.path), []byte{'\n'}); lines != workers+1 {
		t.Fatalf("got %d journal lines, want %d", lines, workers+1)
	}
}
