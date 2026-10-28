package probe

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/bytedance/sonic"
)

type ProbeStore struct{ path string }

type probeSnapshot struct {
	probes  map[string]Probe
	history map[string][]ProbeEvent
	count   int
}

func ProbeOpen(taskDir string) (*ProbeStore, error) {
	if strings.TrimSpace(taskDir) == "" {
		return nil, fmt.Errorf("%w: task directory is required", ProbeErrInvalid)
	}
	path, err := filepath.Abs(filepath.Join(taskDir, ProbeLedgerName))
	if err != nil {
		return nil, err
	}
	return &ProbeStore{path: path}, nil
}

func (s *ProbeStore) ProbeAdd(spec ProbeSpec) (Probe, bool, error) {
	p, err := ProbeNew(spec)
	if err != nil {
		return Probe{}, false, err
	}
	created := false
	err = s.probeLocked(func() error {
		snapshot, err := s.probeRead()
		if err != nil {
			return err
		}
		if existing, ok := snapshot.probes[p.ID]; ok {
			p = existing
			return nil
		}
		p.CreatedAt = time.Now().UTC()
		p.UpdatedAt = p.CreatedAt
		event := ProbeEvent{Version: 1, Sequence: snapshot.count + 1, Kind: "add", ProbeID: p.ID,
			OracleKind: p.OracleKind, At: p.CreatedAt, Proposal: &p, State: ProbeUntested}
		if err := s.probeAppend(event); err != nil {
			return err
		}
		created = true
		return nil
	})
	return p, created, err
}

func (s *ProbeStore) ProbeRecord(id string, spec ProbeRecordSpec) (Probe, error) {
	spec, err := probeValidateRecord(spec)
	if err != nil {
		return Probe{}, err
	}
	var result Probe
	err = s.probeLocked(func() error {
		snapshot, err := s.probeRead()
		if err != nil {
			return err
		}
		p, ok := snapshot.probes[id]
		if !ok {
			return fmt.Errorf("%w: %s", ProbeErrNotFound, id)
		}
		event := ProbeEvent{Version: 1, Sequence: snapshot.count + 1, Kind: "record", ProbeID: id,
			OracleKind: p.OracleKind, At: time.Now().UTC(), State: spec.State, Verdict: spec.Verdict,
			Evidence: spec.Evidence, Note: spec.Note, Assessment: ProbeModelAssessed}
		if err := s.probeAppend(event); err != nil {
			return err
		}
		result = probeApply(p, event)
		return nil
	})
	return result, err
}

func (s *ProbeStore) ProbeNext(limit int, oracle ProbeOracleKind) (ProbeBatch, error) {
	if err := probeValidateLimit(limit); err != nil {
		return ProbeBatch{}, err
	}
	if oracle != "" {
		if _, err := ProbeParseOracle(string(oracle)); err != nil {
			return ProbeBatch{}, err
		}
	}
	snapshot, err := s.probeRead()
	if err != nil {
		return ProbeBatch{}, err
	}
	all := probeUntestedList(snapshot)
	result := ProbeBatch{Probes: []Probe{}, TotalUntested: len(all), FrontierEmpty: len(all) == 0,
		Interpretation: probeInterpretation(len(snapshot.probes), len(all))}
	for _, p := range all {
		if oracle != "" && p.OracleKind != oracle {
			continue
		}
		result.MatchingUntested++
		if len(result.Probes) < limit {
			result.Probes = append(result.Probes, p)
		}
	}
	result.Omitted = result.MatchingUntested - len(result.Probes)
	if result.MatchingUntested == 0 && result.TotalUntested > 0 {
		result.Interpretation = "No untested probes match this oracle. Other planned experiments remain."
	}
	return result, nil
}

func (s *ProbeStore) ProbeCoverage(limit int) (ProbeCoverage, error) {
	if err := probeValidateLimit(limit); err != nil {
		return ProbeCoverage{}, err
	}
	snapshot, err := s.probeRead()
	if err != nil {
		return ProbeCoverage{}, err
	}
	result := ProbeCoverage{Total: len(snapshot.probes), ByState: probeStateCounts(),
		ByOracle: make(map[ProbeOracleKind]ProbeOracleCoverage), Untested: []Probe{}}
	for _, kind := range ProbeOracleKinds() {
		result.ByOracle[kind] = ProbeOracleCoverage{ByState: probeStateCounts()}
	}
	for _, p := range snapshot.probes {
		result.ByState[p.State]++
		counts := result.ByOracle[p.OracleKind]
		counts.Total++
		counts.ByState[p.State]++
		result.ByOracle[p.OracleKind] = counts
	}
	all := probeUntestedList(snapshot)
	result.Untested = all[:min(limit, len(all))]
	result.Omitted = len(all) - len(result.Untested)
	result.FrontierEmpty = len(all) == 0
	result.Interpretation = probeInterpretation(result.Total, len(all))
	return result, nil
}

func (s *ProbeStore) ProbeShow(id string, limit int) (ProbeDetail, error) {
	if err := probeValidateLimit(limit); err != nil {
		return ProbeDetail{}, err
	}
	snapshot, err := s.probeRead()
	if err != nil {
		return ProbeDetail{}, err
	}
	p, ok := snapshot.probes[id]
	if !ok {
		return ProbeDetail{}, fmt.Errorf("%w: %s", ProbeErrNotFound, id)
	}
	history := snapshot.history[id]
	omitted := max(0, len(history)-limit)
	return ProbeDetail{Probe: p, History: history[omitted:], TotalEvents: len(history), OmittedEvents: omitted}, nil
}

func probeUntestedList(snapshot probeSnapshot) []Probe {
	result := []Probe{}
	for _, p := range snapshot.probes {
		if p.State == ProbeUntested {
			result = append(result, p)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Priority != result[j].Priority {
			return result[i].Priority > result[j].Priority
		}
		if !result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].CreatedAt.Before(result[j].CreatedAt)
		}
		return result[i].ID < result[j].ID
	})
	return result
}

func probeApply(p Probe, event ProbeEvent) Probe {
	p.State, p.Verdict, p.Note, p.Assessment = event.State, event.Verdict, event.Note, event.Assessment
	p.UpdatedAt = event.At
	refs := append([]string{}, p.Evidence...)
	p.Evidence = probeUnique(append(refs, event.Evidence...))
	return p
}

func (s *ProbeStore) probeRead() (probeSnapshot, error) {
	snapshot := probeSnapshot{probes: make(map[string]Probe), history: make(map[string][]ProbeEvent)}
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return snapshot, nil
	}
	if err != nil {
		return snapshot, err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, probeMaxRecordBytes)
	for {
		line, err := reader.ReadSlice('\n')
		if err == io.EOF && len(line) == 0 {
			return snapshot, nil
		}
		if err != nil {
			return snapshot, fmt.Errorf("%w at line %d: incomplete or oversized record", ProbeErrCorrupt, snapshot.count+1)
		}
		var event ProbeEvent
		if err := sonic.Unmarshal(line, &event); err != nil {
			return snapshot, fmt.Errorf("%w at line %d: invalid JSON", ProbeErrCorrupt, snapshot.count+1)
		}
		if err := probeValidateEvent(event, snapshot); err != nil {
			return snapshot, fmt.Errorf("%w at line %d: %s", ProbeErrCorrupt, snapshot.count+1, err)
		}
		if event.Kind == "add" {
			snapshot.probes[event.ProbeID] = *event.Proposal
		} else {
			snapshot.probes[event.ProbeID] = probeApply(snapshot.probes[event.ProbeID], event)
		}
		snapshot.history[event.ProbeID] = append(snapshot.history[event.ProbeID], event)
		snapshot.count++
	}
}

func probeValidateEvent(event ProbeEvent, snapshot probeSnapshot) error {
	if event.Version != 1 || event.Sequence != snapshot.count+1 || event.At.IsZero() {
		return fmt.Errorf("invalid version, sequence, or timestamp")
	}
	if _, err := ProbeParseOracle(string(event.OracleKind)); err != nil {
		return err
	}
	p, exists := snapshot.probes[event.ProbeID]
	switch event.Kind {
	case "add":
		if event.Proposal == nil || exists {
			return fmt.Errorf("missing or duplicate proposal")
		}
		p = *event.Proposal
		if p.ID != event.ProbeID || p.OracleKind != event.OracleKind || p.State != ProbeUntested || event.State != ProbeUntested ||
			len(p.Evidence) != 0 || p.Verdict != "" || p.Assessment != "" || p.Note != "" ||
			p.Hypothesis == "" || p.Precondition == "" || p.EndpointFamily == "" || p.MutationShape == "" ||
			!probeRequestID.MatchString(p.RequestRef) || !p.CreatedAt.Equal(event.At) || !p.UpdatedAt.Equal(event.At) ||
			p.Priority != probePriority(p.OracleKind) || p.ID != probeContentID(p.EndpointFamily, p.MutationShape, p.IdentityShape, p.OracleKind) {
			return fmt.Errorf("invalid proposal identity or initial state")
		}
		if event.Verdict != "" || event.Note != "" || len(event.Evidence) != 0 || event.Assessment != "" {
			return fmt.Errorf("proposal cannot contain a verdict")
		}
	case "record":
		if !exists || event.Proposal != nil || p.OracleKind != event.OracleKind || event.Assessment != ProbeModelAssessed {
			return fmt.Errorf("record does not match an existing proposal or assessment basis")
		}
		spec := ProbeRecordSpec{Verdict: event.Verdict, Evidence: event.Evidence, Note: event.Note}
		if event.Verdict == "" {
			spec.State = event.State
		}
		validated, err := probeValidateRecord(spec)
		if err != nil || validated.State != event.State {
			return fmt.Errorf("invalid verdict, state, or evidence pointers")
		}
	default:
		return fmt.Errorf("unknown event kind")
	}
	return nil
}

// Rename replaces the inode, so writers must lock a separate, stable file.
// Readers see a complete old or new snapshot without creating files themselves.
func (s *ProbeStore) probeLocked(fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn()
}

// Preserve the existing bytes verbatim. A failed append must never publish a
// partial JSON line or rewrite an earlier occurrence during serialization.
func (s *ProbeStore) probeAppend(event ProbeEvent) error {
	data, err := sonic.Marshal(event)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > probeMaxRecordBytes {
		return fmt.Errorf("%w: record exceeds %d bytes", ProbeErrInvalid, probeMaxRecordBytes)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".probes-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	old, err := os.Open(s.path)
	if err == nil {
		_, copyErr := io.Copy(tmp, old)
		closeErr := old.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
