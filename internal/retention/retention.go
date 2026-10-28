package retention

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/store"
)

const (
	RetentionEvidence              = "evidence"
	RetentionPrimary               = "primary"
	RetentionAmbient               = "ambient"
	RetentionDefaultMaxBytes int64 = 256 << 20
	RetentionDefaultKeepDays       = 30
	gcReportLimit                  = 100
)

type RetentionOptions struct {
	MaxBytes int64
	KeepDays int
	Now      time.Time
	Status   bool
}

type RetentionUsage struct {
	Records          int   `json:"records"`
	BodyBytes        int64 `json:"body_bytes"`
	StorageBytes     int64 `json:"storage_bytes"`
	ProtectedRecords int   `json:"protected_records"`
	ProtectedBytes   int64 `json:"protected_body_bytes"`
	PlannedRecords   int   `json:"planned_records"`
	PlannedBodyBytes int64 `json:"planned_body_bytes"`
	PlannedReclaimed int64 `json:"planned_bytes_reclaimed"`
	EvictedRecords   int   `json:"evicted_records"`
	EvictedBodyBytes int64 `json:"evicted_body_bytes"`
	ReclaimedBytes   int64 `json:"bytes_reclaimed"`
}

type RetentionProtection struct {
	Records      int   `json:"records"`
	Bytes        int64 `json:"body_bytes"`
	Files        int   `json:"files"`
	StorageBytes int64 `json:"storage_bytes"`
}

type RetentionSkip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Bytes  int64  `json:"bytes"`
}

type RetentionEviction struct {
	File      string `json:"file"`
	RequestID string `json:"request_id"`
	Class     string `json:"class"`
	Reason    string `json:"reason"`
	BodyBytes int64  `json:"body_bytes"`
	Reclaimed int64  `json:"bytes_reclaimed"`
}

type RetentionReport struct {
	Workspace        string                          `json:"workspace,omitempty"`
	Task             string                          `json:"task"`
	TotalBytes       int64                           `json:"total_bytes"`
	MetadataBytes    int64                           `json:"metadata_and_unmanaged_bytes"`
	Classes          map[string]*RetentionUsage      `json:"classes"`
	Protected        map[string]*RetentionProtection `json:"protected"`
	Skipped          []RetentionSkip                 `json:"skipped"`
	SkippedOmitted   int                             `json:"skipped_omitted"`
	Evictions        []RetentionEviction             `json:"evictions"`
	EvictionsOmitted int                             `json:"evictions_omitted"`
	Policy           RetentionOptionsView            `json:"policy"`
	PlannedReclaimed int64                           `json:"planned_bytes_reclaimed"`
	ReclaimedBytes   int64                           `json:"bytes_reclaimed"`
	RemainingBytes   int64                           `json:"remaining_bytes"`
	BudgetUnmetBytes int64                           `json:"budget_unmet_bytes"`
	Applied          bool                            `json:"applied"`
	SnapshotAt       time.Time                       `json:"snapshot_at"`
}

type RetentionOptionsView struct {
	MaxBytes        int64 `json:"max_bytes"`
	PrimaryKeepDays int   `json:"primary_keep_days"`
	AmbientKeepDays int   `json:"ambient_keep_days"`
}

type RetentionPlan struct {
	Report     *RetentionReport
	dir        string
	options    RetentionOptions
	references map[string]os.FileInfo
	files      []*gcFile
}

type gcContainer struct {
	id, hash, captureID, digest, note string
	timestamp                         int64
}

type gcRecord struct {
	start, end                                                          int64
	id, originalID, sid, url, sourceSessionID, class, protected, reason string
	requestBodyHash, responseBodyHash                                   string
	timestamp, bodyBytes, ageSavings, budgetSavings                     int64
	group                                                               *gcContainer
	file                                                                *gcFile
}

type gcFile struct {
	name, path string
	stamp      os.FileInfo
	records    []*gcRecord
	skipped    string
}

// Raw messages preserve unknown metadata and future provenance fields. Only
// selected request objects are rewritten; protected occurrences remain verbatim.
type gcRawMessage []byte

func (raw gcRawMessage) MarshalJSON() ([]byte, error) { return raw, nil }
func (raw *gcRawMessage) UnmarshalJSON(data []byte) error {
	*raw = append((*raw)[:0], data...)
	return nil
}

func RetentionBuildPlan(dir string, options RetentionOptions) (*RetentionPlan, error) {
	if options.MaxBytes < 0 || options.KeepDays < 0 || options.KeepDays > 365000 {
		return nil, fmt.Errorf("max-bytes must be nonnegative and keep-days must be between 0 and 365000")
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	report := &RetentionReport{
		Task: filepath.Base(dir), Classes: map[string]*RetentionUsage{}, Protected: map[string]*RetentionProtection{},
		Skipped: []RetentionSkip{}, Evictions: []RetentionEviction{}, SnapshotAt: options.Now,
		Policy: RetentionOptionsView{options.MaxBytes, options.KeepDays, min(options.KeepDays, 1)},
	}
	for _, class := range []string{RetentionEvidence, RetentionPrimary, RetentionAmbient} {
		report.Classes[class] = &RetentionUsage{}
	}
	plan := &RetentionPlan{Report: report, dir: dir, options: options}
	entries, err := gcInventory(dir)
	if err != nil {
		return nil, err
	}
	refs, locks, stamps, err := gcReferences(dir, entries)
	if err != nil {
		return nil, err
	}
	defer gcUnlock(locks)
	plan.references = stamps
	primary := map[string]bool{}
	var accounted int64
	for _, name := range []string{"store.json", "sessions.jsonl", "live.json"} {
		info := entries[name]
		if info == nil {
			continue
		}
		file := &gcFile{name: name, path: filepath.Join(dir, name), stamp: info}
		plan.files = append(plan.files, file)
		if !info.Mode().IsRegular() {
			file.skipped = "non_regular_or_symlink"
			report.gcSkip(name, file.skipped, info.Size())
			continue
		}
		if name == "live.json" {
			file.skipped = "live_capture_may_be_written"
		} else if name == "sessions.jsonl" && gcSessionStaged(entries) {
			file.skipped = "session_append_in_progress"
		}
		handle, err := gcOpenLocked(filepath.Join(dir, name), false)
		if err != nil {
			file.skipped = "currently_being_written: " + err.Error()
			report.gcSkip(name, file.skipped, info.Size())
			continue
		}
		if file.skipped == "" {
			writing, writerErr := gcHasWriter(filepath.Join(dir, name))
			if writerErr != nil || writing {
				file.skipped = "currently_being_written"
				if writerErr != nil {
					file.skipped = "writer_check_unavailable: " + writerErr.Error()
				}
			}
		}
		err = gcScanCapture(handle, file, options, primary)
		current, statErr := handle.Stat()
		gcUnlock([]*os.File{handle})
		if statErr != nil || !gcSame(info, current) {
			file.records = nil
			file.skipped = "changed_during_scan"
		} else if err != nil {
			file.records = nil
			file.skipped = "unreadable_capture: " + err.Error()
		}
		if file.skipped != "" {
			report.gcSkip(name, file.skipped, info.Size())
		}
	}
	for name, info := range entries {
		if !info.Mode().IsRegular() {
			continue
		}
		report.TotalBytes += info.Size()
		if strings.HasPrefix(name, "evidence/") || gcReferenceName(name) {
			report.Classes[RetentionEvidence].StorageBytes += info.Size()
			accounted += info.Size()
			protection := report.Protected["immutable_evidence_record_or_artifact"]
			if protection == nil {
				protection = &RetentionProtection{}
				report.Protected["immutable_evidence_record_or_artifact"] = protection
			}
			protection.Files++
			protection.StorageBytes += info.Size()
		}
		if name != "live.json" && (strings.HasSuffix(name, ".tmp") || strings.HasPrefix(filepath.Base(name), ".rep-record-spool-")) {
			report.gcSkip(name, "capture_staging_or_unmanaged_file", info.Size())
		}
	}
	var candidates []*gcRecord
	for _, file := range plan.files {
		for _, record := range file.records {
			record.class, record.protected = gcClassify(record, primary, refs)
			usage := report.Classes[record.class]
			usage.Records++
			usage.BodyBytes += record.bodyBytes
			usage.StorageBytes += record.end - record.start
			accounted += record.end - record.start
			if record.protected == "" && file.skipped != "" {
				record.protected = file.skipped
			}
			if record.protected == "" && record.timestamp == 0 {
				record.timestamp = record.group.timestamp
			}
			if record.protected != "" {
				usage.ProtectedRecords++
				usage.ProtectedBytes += record.bodyBytes
				protection := report.Protected[record.protected]
				if protection == nil {
					protection = &RetentionProtection{}
					report.Protected[record.protected] = protection
				}
				protection.Records++
				protection.Bytes += record.bodyBytes
			} else if record.bodyBytes > 0 {
				candidates = append(candidates, record)
			}
		}
	}
	report.MetadataBytes = report.TotalBytes - accounted
	report.RemainingBytes = report.TotalBytes
	if options.Status {
		report.BudgetUnmetBytes = max(0, report.RemainingBytes-options.MaxBytes)
		return plan, nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].class != candidates[j].class {
			return candidates[i].class == RetentionAmbient
		}
		return candidates[i].timestamp < candidates[j].timestamp
	})
	for _, record := range candidates {
		days := options.KeepDays
		if record.class == RetentionAmbient {
			days = min(days, 1)
		}
		if record.timestamp > 0 && options.Now.UnixMilli()-record.timestamp >= int64(days)*86400000 {
			record.reason = "retention_age"
			report.gcSelect(record, record.ageSavings)
		}
	}
	for _, record := range candidates {
		if report.RemainingBytes <= options.MaxBytes {
			break
		}
		if record.reason == "" && record.budgetSavings > 0 {
			record.reason = "retention_budget"
			report.gcSelect(record, record.budgetSavings)
		}
	}
	report.BudgetUnmetBytes = max(0, report.RemainingBytes-options.MaxBytes)
	return plan, nil
}

func (report *RetentionReport) gcSkip(path, reason string, size int64) {
	if len(report.Skipped) >= gcReportLimit {
		report.SkippedOmitted++
		return
	}
	report.Skipped = append(report.Skipped, RetentionSkip{path, reason, size})
}

func (report *RetentionReport) gcSelect(record *gcRecord, savings int64) {
	usage := report.Classes[record.class]
	usage.PlannedRecords++
	usage.PlannedBodyBytes += record.bodyBytes
	usage.PlannedReclaimed += savings
	report.PlannedReclaimed += savings
	report.RemainingBytes -= savings
	if len(report.Evictions) >= gcReportLimit {
		report.EvictionsOmitted++
	} else {
		report.Evictions = append(report.Evictions, RetentionEviction{record.file.name, record.id, record.class, record.reason, record.bodyBytes, savings})
	}
}

func gcScanCapture(handle io.Reader, file *gcFile, options RetentionOptions, primary map[string]bool) error {
	reader := gcNewReader(handle)
	for count := 0; ; count++ {
		err := reader.gcSpace()
		if err == io.EOF {
			if count == 0 {
				return fmt.Errorf("empty capture file")
			}
			return nil
		}
		if err != nil {
			return err
		}
		if count > 0 && !strings.HasSuffix(file.name, ".jsonl") {
			return fmt.Errorf("multiple JSON documents")
		}
		if err := gcScanContainer(reader, &gcContainer{}, file, options, primary, 0); err != nil {
			return err
		}
	}
}

func gcScanContainer(reader *gcJSONReader, group *gcContainer, file *gcFile, options RetentionOptions, primary map[string]bool, depth int) error {
	if depth > 4 {
		return fmt.Errorf("nested capture containers")
	}
	return reader.gcObject(func(key string) error {
		switch key {
		case "requests":
			return reader.gcArray(func() error {
				if err := reader.gcSpace(); err != nil {
					return err
				}
				start := reader.offset
				raw, err := reader.gcRaw()
				if err != nil {
					return err
				}
				var request store.Request
				if len(raw) == 0 || raw[0] != '{' {
					return fmt.Errorf("request must be an object")
				}
				if err := sonic.Unmarshal(raw, &request); err != nil {
					return err
				}
				bytes := int64(len(request.Body))
				if request.Response != nil {
					bytes += int64(len(request.Response.Body))
				}
				if request.Stream != nil {
					for _, event := range request.Stream.Events {
						bytes += int64(len(event.Payload))
					}
				}
				record := &gcRecord{start: start, end: reader.offset, id: request.ID, originalID: request.OriginalID,
					sid: string(store.GenerateSemanticID(&request)), url: request.URL, sourceSessionID: request.SourceSessionID, timestamp: request.Timestamp,
					bodyBytes: bytes, group: group, file: file}
				record.class = gcRequestClass(&request, primary)
				if request.RequestBodyCapture != nil {
					record.requestBodyHash = request.RequestBodyCapture.SHA256
				}
				if request.ResponseBodyCapture != nil {
					record.responseBodyHash = request.ResponseBodyCapture.SHA256
				}
				if bytes > 0 && !options.Status {
					for _, reason := range []string{"retention_age", "retention_budget"} {
						// Class labels have equal length, allowing classification after
						// later session metadata and primary settings have been read.
						updated, err := gcEvict(raw, options.Now, record.class, reason)
						if err != nil {
							return err
						}
						savings := int64(len(raw) - len(updated))
						if reason == "retention_age" {
							record.ageSavings = savings
						} else {
							record.budgetSavings = savings
						}
					}
				}
				file.records = append(file.records, record)
				return nil
			})
		case "session":
			if err := reader.gcSpace(); err != nil {
				return err
			}
			if value, _ := reader.gcPeek(); value == 'n' {
				_, err := reader.gcRaw()
				return err
			}
			return gcScanContainer(reader, &gcContainer{}, file, options, primary, depth+1)
		case "sessions":
			return reader.gcArray(func() error { return gcScanContainer(reader, &gcContainer{}, file, options, primary, depth+1) })
		default:
			raw, err := reader.gcRaw()
			if err != nil {
				return err
			}
			switch key {
			case "id":
				return sonic.Unmarshal(raw, &group.id)
			case "hash_id":
				return sonic.Unmarshal(raw, &group.hash)
			case "capture_session_id", "session_id":
				return sonic.Unmarshal(raw, &group.captureID)
			case "timestamp":
				return sonic.Unmarshal(raw, &group.timestamp)
			case "note":
				return sonic.Unmarshal(raw, &group.note)
			case "capture_digest":
				return sonic.Unmarshal(raw, &group.digest)
			case "primary_domains":
				return sonic.Unmarshal(raw, &primary)
			}
			return nil
		}
	})
}

func gcRequestClass(request *store.Request, primary map[string]bool) string {
	parsed, err := url.Parse(request.URL)
	if err != nil || parsed.Hostname() == "" {
		return RetentionPrimary
	}
	ext := strings.ToLower(filepath.Ext(parsed.Path))
	switch ext {
	case ".woff", ".woff2", ".ttf", ".otf", ".eot", ".css", ".js", ".mjs", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".svg", ".ico", ".map", ".mp4", ".webm", ".mp3":
		return RetentionAmbient
	}
	contentType := ""
	if request.Response != nil {
		contentType = strings.ToLower(store.HeaderFirst(request.Response.Headers, "content-type"))
	}
	if strings.HasPrefix(contentType, "image/") || strings.HasPrefix(contentType, "font/") || strings.HasPrefix(contentType, "video/") || strings.HasPrefix(contentType, "audio/") || strings.Contains(contentType, "javascript") || strings.HasPrefix(contentType, "text/css") || strings.Contains(contentType, "font") {
		return RetentionAmbient
	}
	switch strings.ToLower(request.ResourceType) {
	case "font", "stylesheet", "script", "image", "media":
		return RetentionAmbient
	}
	for domain, enabled := range primary {
		domain = strings.ToLower(strings.TrimSuffix(domain, "."))
		host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
		if enabled && (host == domain || strings.HasSuffix(host, "."+domain) || strings.EqualFold(parsed.Host, domain)) {
			return RetentionPrimary
		}
	}
	// Without a configured scope, unknown dynamic traffic gets long retention.
	for _, enabled := range primary {
		if enabled {
			return RetentionAmbient
		}
	}
	return RetentionPrimary
}

func gcClassify(record *gcRecord, primary map[string]bool, refs map[string]string) (string, string) {
	group := record.group
	identifiers := []string{record.id, record.originalID, record.sid, record.url, record.sourceSessionID, group.id, group.hash, group.captureID, group.digest, record.requestBodyHash, record.responseBodyHash, record.file.name, record.file.path}
	if group.id != "" {
		identifiers = append(identifiers, store.GenerateHashID("s", group.id, strconv.FormatInt(group.timestamp, 10)))
	}
	for _, identifier := range identifiers {
		if identifier == "" {
			continue
		}
		if source := refs[identifier]; source != "" {
			return RetentionEvidence, "referenced_by:" + source
		}
		if strings.HasPrefix(identifier, "h_") || strings.HasPrefix(identifier, "s_") {
			for _, variant := range []string{identifier, strings.TrimPrefix(strings.TrimPrefix(identifier, "h_"), "s_")} {
				for size := len(variant); size >= 4; size-- {
					if source := refs[variant[:size]]; source != "" {
						return RetentionEvidence, "referenced_by:" + source
					}
				}
			}
		}
		if identifier == record.id || identifier == record.originalID {
			for size := len(identifier) - 1; size >= 4; size-- {
				if source := refs[identifier[:size]]; source != "" {
					return RetentionEvidence, "referenced_by:" + source
				}
			}
		}
	}
	if strings.TrimSpace(group.note) != "" {
		return RetentionEvidence, "session_note"
	}
	// Static classification is already known; only third-party classification
	// depends on settings potentially serialized after the requests array.
	if record.class == RetentionAmbient {
		return record.class, ""
	}
	return gcRequestClass(&store.Request{URL: record.url}, primary), ""
}

func gcEvict(raw []byte, now time.Time, class, reason string) ([]byte, error) {
	var request map[string]gcRawMessage
	if err := sonic.Unmarshal(raw, &request); err != nil {
		return nil, err
	}
	marker := map[string]any{"state": "evicted", "class": class, "reason": reason, "evicted_at": now.UTC().Format(time.RFC3339Nano)}
	var response map[string]gcRawMessage
	if body := request["response"]; len(body) > 0 && string(body) != "null" {
		if err := sonic.Unmarshal(body, &response); err != nil {
			return nil, err
		}
	}
	for _, side := range []string{"request", "response"} {
		fields := request
		if side == "response" {
			fields = response
		}
		var body string
		if len(fields["body"]) == 0 {
			continue
		}
		if err := sonic.Unmarshal(fields["body"], &body); err != nil {
			return nil, err
		}
		if body == "" {
			continue
		}
		delete(fields, "body")
		key := side + "_body_capture"
		capture := map[string]gcRawMessage{}
		if prior := request[key]; len(prior) > 0 && string(prior) != "null" {
			if err := sonic.Unmarshal(prior, &capture); err != nil {
				return nil, err
			}
			marker["original_"+key] = prior
		}
		marker[side+"_body_bytes"] = len(body)
		// Existing body readers understand unavailable and retain its reason.
		// The old digest describes the evicted bytes, not the new empty value.
		delete(capture, "sha256")
		capture["state"], _ = sonic.Marshal("unavailable")
		capture["reason"], _ = sonic.Marshal("retention_evicted: " + reason)
		capture["captured_bytes"] = gcRawMessage("0")
		request[key], _ = sonic.Marshal(capture)
		if side == "response" {
			request["response_body_error"], _ = sonic.Marshal("retention_evicted: " + reason)
		}
	}
	if response != nil {
		request["response"], _ = sonic.Marshal(response)
	}
	if len(request["stream"]) > 0 && string(request["stream"]) != "null" {
		var stream map[string]gcRawMessage
		if err := sonic.Unmarshal(request["stream"], &stream); err != nil {
			return nil, err
		}
		var events []map[string]gcRawMessage
		if err := sonic.Unmarshal(stream["events"], &events); err != nil {
			return nil, err
		}
		var bytes int64
		for _, event := range events {
			if len(event["payload"]) == 0 {
				continue
			}
			var payload string
			if err := sonic.Unmarshal(event["payload"], &payload); err != nil {
				return nil, err
			}
			if payload == "" {
				continue
			}
			bytes += int64(len(payload))
			delete(event, "payload")
			event["retention_evicted"], _ = sonic.Marshal(reason)
		}
		if bytes > 0 {
			marker["stream_payload_bytes"] = bytes
			marker["original_stream_capture"] = stream["capture"]
			var capture map[string]gcRawMessage
			if err := sonic.Unmarshal(stream["capture"], &capture); err != nil {
				return nil, err
			}
			capture["state"], _ = sonic.Marshal("unavailable")
			capture["reason"], _ = sonic.Marshal("retention_evicted: " + reason)
			capture["captured_bytes"] = gcRawMessage("0")
			stream["capture"], _ = sonic.Marshal(capture)
			stream["events"], _ = sonic.Marshal(events)
			request["stream"], _ = sonic.Marshal(stream)
		}
	}
	request["retention"], _ = sonic.Marshal(marker)
	return sonic.Marshal(request)
}
