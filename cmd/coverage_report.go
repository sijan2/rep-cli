package cmd

import (
	"math"
	"net/url"
	"sort"
	"strings"

	"github.com/repplus/rep-cli/internal/store"
)

// CaptureCoverageReport describes one retained capture, without reading external
// resources or disclosing URLs, headers, payloads, or arbitrary collector labels.
// A complete projection is a complete aggregate report, not a complete website.
type CaptureCoverageReport struct {
	Version          int                       `json:"version"`
	Source           string                    `json:"source"`
	Observed         captureObservedCounts     `json:"observed"`
	ResponseBodies   captureBodyCounts         `json:"response_bodies"`
	RequestBodies    captureBodyCounts         `json:"request_bodies"`
	Streams          captureStreamCounts       `json:"streams"`
	Capture          captureCollectionCoverage `json:"capture"`
	ProtocolEvidence []captureProtocolEvidence `json:"protocol_evidence"`
	Projection       captureReportProjection   `json:"projection"`
	SiteCoverage     string                    `json:"site_coverage"`
	WireEvidence     string                    `json:"wire_evidence"`
	AccountIsolation string                    `json:"account_isolation"`
}

type captureObservedCounts struct {
	Records        int `json:"records"`
	HTTPRequests   int `json:"http_requests"`
	StreamRecords  int `json:"stream_records"`
	UnknownRecords int `json:"unknown_records"`
	Responses      int `json:"responses"`
	Pages          int `json:"page_url_references"`
	Scripts        int `json:"script_requests"`
	UniqueScripts  int `json:"unique_script_urls"`
	MissingPageURL int `json:"records_without_page_url"`
	UnfinishedHTTP int `json:"unfinished_http_requests"`
}

type captureBodyCounts struct {
	States                    map[string]int `json:"states"`
	MetadataMissing           int            `json:"metadata_missing"`
	MetadataInvalid           int            `json:"metadata_invalid"`
	LegacyLossSignals         int            `json:"legacy_loss_signals,omitempty"`
	DeclaredEmpty             int            `json:"declared_empty"`
	DeclaredCapturedBytes     int64          `json:"declared_captured_bytes"`
	StoredRepresentationBytes int64          `json:"stored_representation_bytes"`
	PayloadValidation         string         `json:"payload_validation"`
	CountersSaturated         bool           `json:"counters_saturated,omitempty"`
}

type captureStreamCounts struct {
	States            map[string]int `json:"states"`
	MetadataMissing   int            `json:"metadata_missing"`
	MetadataInvalid   int            `json:"metadata_invalid"`
	Open              int            `json:"open_or_connecting"`
	WithLimitations   int            `json:"with_declared_limitations"`
	CapturedEvents    int64          `json:"declared_captured_events"`
	ObservedEvents    int64          `json:"declared_observed_events"`
	DroppedEvents     int64          `json:"declared_dropped_events"`
	CapturedBytes     int64          `json:"declared_captured_bytes"`
	ObservedBytes     int64          `json:"declared_observed_bytes"`
	RetainedGapEvents int            `json:"retained_gap_events"`
	TruncatedEvents   int            `json:"truncated_events"`
	PayloadValidation string         `json:"payload_validation"`
	CountersSaturated bool           `json:"counters_saturated,omitempty"`
}

type captureCollectionCoverage struct {
	State           string           `json:"state"`
	MetadataPresent bool             `json:"metadata_present"`
	Finished        bool             `json:"finished"`
	ExpectedRecords *int             `json:"expected_records,omitempty"`
	ReceivedRecords *int             `json:"received_records,omitempty"`
	DroppedRecords  int64            `json:"reported_dropped_records"`
	TimedOut        bool             `json:"timed_out"`
	ErrorReported   bool             `json:"error_reported"`
	WarningsTotal   int              `json:"warnings_total"`
	WarningCodes    []string         `json:"warning_codes"`
	WarningsOmitted int              `json:"warning_details_omitted"`
	Stats           map[string]int64 `json:"reported_stats"`
	Limits          map[string]int64 `json:"reported_limits"`
	MetadataInvalid int              `json:"metadata_invalid"`
	GapSignals      []string         `json:"gap_signals"`
	Unknowns        []string         `json:"unknowns"`
}

type captureProtocolEvidence struct {
	Protocol   string `json:"protocol"`
	Source     string `json:"source"`
	Trust      string `json:"trust"`
	Visibility string `json:"visibility"`
	Records    int    `json:"records"`
}

type captureReportProjection struct {
	Complete               bool `json:"complete"`
	RecordsAggregated      int  `json:"records_aggregated"`
	MetadataDetailsOmitted int  `json:"metadata_details_omitted"`
	ProtocolGroupsOmitted  int  `json:"protocol_groups_omitted"`
}

// captureCoverageExport preserves collector metadata when an archive is selected.
func captureCoverageExport(session *store.Session) store.Export {
	if session == nil {
		return store.Export{}
	}
	return store.Export{Requests: session.Requests, SessionID: session.CaptureSessionID,
		BrowserSession: session.BrowserSession, CaptureDigest: session.CaptureDigest}
}

// buildCaptureCoverage requires the full selected capture, before display filters.
// Counts copied from collector metadata are explicitly declared, not validated
// payload integrity. Call body/stream --require-complete for individual evidence.
func buildCaptureCoverage(export store.Export, source string) CaptureCoverageReport {
	if source != "live" && source != "saved" && source != "no_capture" {
		source = "unknown"
	}
	report := CaptureCoverageReport{
		Version: 1, Source: source,
		ResponseBodies: captureBodyCounts{States: map[string]int{}, PayloadValidation: "not_checked"},
		RequestBodies:  captureBodyCounts{States: map[string]int{}, PayloadValidation: "not_checked"},
		Streams:        captureStreamCounts{States: map[string]int{}, PayloadValidation: "not_checked"},
		Capture: captureCollectionCoverage{State: "unknown", WarningCodes: []string{},
			Stats: map[string]int64{}, Limits: map[string]int64{}, GapSignals: []string{}, Unknowns: []string{}},
		ProtocolEvidence: []captureProtocolEvidence{},
		Projection:       captureReportProjection{Complete: true, RecordsAggregated: len(export.Requests)},
		SiteCoverage:     "unknown; unvisited features and earlier traffic are not observed",
		WireEvidence:     "not established by browser request or stream records; independent native artifacts require separate review",
		AccountIsolation: "task namespaces separate captured data; account state is shared within each browser profile",
	}
	pages, scripts := map[string]bool{}, map[string]bool{}
	evidence := map[string]*captureProtocolEvidence{}
	for index := range export.Requests {
		request := &export.Requests[index]
		report.Observed.Records++
		if request.PageURL != "" {
			pages[strings.SplitN(request.PageURL, "#", 2)[0]] = true
		} else {
			report.Observed.MissingPageURL++
		}
		if strings.EqualFold(request.ResourceType, "document") && request.URL != "" {
			pages[strings.SplitN(request.URL, "#", 2)[0]] = true
		}
		if store.IsStreamKind(request.RecordKind) {
			report.Observed.StreamRecords++
			captureAccumulateStream(&report.Streams, request)
		} else if request.RecordKind == "" || request.RecordKind == "http" {
			report.Observed.HTTPRequests++
			if request.Response != nil {
				report.Observed.Responses++
			}
			if request.NetworkState == "pending" {
				report.Observed.UnfinishedHTTP++
			}
			if captureIsScript(request) {
				report.Observed.Scripts++
				if request.URL != "" {
					scripts[request.URL] = true
				}
			}
			captureAccumulateBody(&report.RequestBodies, request, true)
			captureAccumulateBody(&report.ResponseBodies, request, false)
		} else {
			report.Observed.UnknownRecords++
		}
		item := captureProtocolFor(request)
		key := item.Protocol + ":" + item.Source
		if previous := evidence[key]; previous != nil {
			previous.Records++
		} else {
			item.Records = 1
			evidence[key] = &item
		}
	}
	report.Observed.Pages, report.Observed.UniqueScripts = len(pages), len(scripts)
	keys := make([]string, 0, len(evidence))
	for key := range evidence {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for index, key := range keys {
		if index < 12 {
			report.ProtocolEvidence = append(report.ProtocolEvidence, *evidence[key])
		} else {
			report.Projection.ProtocolGroupsOmitted++
		}
	}
	captureSummarizeCollection(&report, export.BrowserSession)
	report.Projection.Complete = report.Projection.MetadataDetailsOmitted == 0 && report.Projection.ProtocolGroupsOmitted == 0
	return report
}

func captureIsScript(request *store.Request) bool {
	if request == nil || (request.RecordKind != "" && request.RecordKind != "http") {
		return false
	}
	if strings.EqualFold(request.ResourceType, "script") {
		return true
	}
	if parsed, err := url.Parse(request.URL); err == nil {
		path := strings.ToLower(parsed.Path)
		if strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".mjs") || strings.HasSuffix(path, ".cjs") {
			return true
		}
	}
	if request.Response != nil {
		kind := strings.ToLower(store.HeaderFirst(request.Response.Headers, "content-type"))
		return strings.Contains(kind, "javascript") || strings.Contains(kind, "ecmascript")
	}
	return false
}

func captureAccumulateBody(counts *captureBodyCounts, request *store.Request, outgoing bool) {
	if counts.States == nil {
		counts.States = map[string]int{}
	}
	if counts.PayloadValidation == "" {
		counts.PayloadValidation = "not_checked"
	}
	metadata, value, encoding := request.RequestBodyCapture, request.Body, ""
	legacyLoss := !outgoing && (request.ResponseBodyTruncated || request.ResponseBodyError != "")
	if legacyLoss {
		counts.LegacyLossSignals++
	}
	if !outgoing {
		metadata, value, encoding = request.ResponseBodyCapture, "", request.ResponseEncoding
		if request.Response != nil {
			value = request.Response.Body
		}
	}
	counts.CountersSaturated = captureAdd(&counts.StoredRepresentationBytes, int64(len(value))) || counts.CountersSaturated
	state := "unknown"
	if metadata == nil {
		counts.MetadataMissing++
		if !outgoing && request.ResponseBodyTruncated {
			state = "partial"
		} else if !outgoing && (request.ResponseBodyError != "" || request.ErrorText != "") {
			state = "unavailable"
			if value != "" {
				state = "partial"
			}
		}
	} else {
		state = browserBodyState(metadata.State)
		if encoding == "" {
			encoding = metadata.Encoding
		}
		invalid := (state == "unknown" && metadata.State != "unknown") || metadata.CapturedBytes < 0 ||
			(metadata.ObservedBytes != nil && *metadata.ObservedBytes < metadata.CapturedBytes) ||
			(metadata.ExpectedBytes != nil && *metadata.ExpectedBytes < 0)
		if encoding == "" || encoding == "utf8" || encoding == "utf-8" || encoding == "text" {
			invalid = invalid || metadata.CapturedBytes != int64(len(value))
		} else if encoding != "base64" {
			invalid = true
		}
		if state == "complete" {
			invalid = invalid || metadata.Reason != "" ||
				(metadata.ObservedBytes != nil && *metadata.ObservedBytes != metadata.CapturedBytes) ||
				(!outgoing && request.Response == nil) || legacyLoss
		}
		if state == "not_applicable" || state == "unavailable" {
			invalid = invalid || metadata.CapturedBytes != 0 || value != ""
		}
		if state == "not_applicable" {
			invalid = invalid || legacyLoss || (metadata.ObservedBytes != nil && *metadata.ObservedBytes != 0)
		}
		if invalid {
			counts.MetadataInvalid++
			state = "unknown"
		} else {
			counts.CountersSaturated = captureAdd(&counts.DeclaredCapturedBytes, metadata.CapturedBytes) || counts.CountersSaturated
			if state == "complete" && metadata.CapturedBytes == 0 && value == "" {
				counts.DeclaredEmpty++
			}
		}
	}
	counts.States[state]++
}

func captureAccumulateStream(counts *captureStreamCounts, request *store.Request) {
	stream := request.Stream
	if stream == nil {
		counts.MetadataMissing++
		counts.States["unknown"]++
		return
	}
	state := browserBodyState(stream.Capture.State)
	if state == "not_applicable" || stream.Version != 1 || stream.Protocol != request.RecordKind || validateStreamCoverage(stream) != nil {
		counts.MetadataInvalid++
		state = "unknown"
	}
	counts.States[state]++
	if stream.State == "open" || stream.State == "connecting" {
		counts.Open++
	}
	if len(stream.Capture.Limitations) > 0 {
		counts.WithLimitations++
	}
	counts.CountersSaturated = captureAdd(&counts.CapturedEvents, stream.Capture.CapturedEvents) || counts.CountersSaturated
	counts.CountersSaturated = captureAdd(&counts.ObservedEvents, stream.Capture.ObservedEvents) || counts.CountersSaturated
	counts.CountersSaturated = captureAdd(&counts.DroppedEvents, stream.Capture.DroppedEvents) || counts.CountersSaturated
	counts.CountersSaturated = captureAdd(&counts.CapturedBytes, stream.Capture.CapturedBytes) || counts.CountersSaturated
	counts.CountersSaturated = captureAdd(&counts.ObservedBytes, stream.Capture.ObservedBytes) || counts.CountersSaturated
	for _, event := range stream.Events {
		if event.Kind == "gap" {
			counts.RetainedGapEvents++
		}
		if event.Truncated {
			counts.TruncatedEvents++
		}
	}
}

func captureProtocolFor(request *store.Request) captureProtocolEvidence {
	item := captureProtocolEvidence{Protocol: "http", Source: "unknown", Trust: "unknown", Visibility: "browser_http_content_and_metadata"}
	if request.RecordKind != "" && request.RecordKind != "http" && !store.IsStreamKind(request.RecordKind) {
		item.Protocol, item.Visibility = "unknown", "unknown"
		return item
	}
	if !store.IsStreamKind(request.RecordKind) {
		if request.CaptureSource == "cdp" {
			item.Source, item.Trust = "cdp", "browser_reported"
		}
		return item
	}
	item.Protocol, item.Visibility = request.RecordKind, "unknown"
	stream := request.Stream
	if stream == nil {
		return item
	}
	switch {
	case stream.Source == "page_api":
		item.Source, item.Trust, item.Visibility = "page_api", "page_controlled", "instrumented_api_calls_only"
	case stream.Source == "cdp_lifecycle":
		item.Source, item.Trust, item.Visibility = "cdp_lifecycle", "browser_reported", "connection_lifecycle_only"
	case stream.Source == "browser_media_recorder":
		item.Source, item.Trust, item.Visibility = "browser_media_recorder", "browser_reported", "reencoded_media_interval"
	case request.RecordKind == "websocket" && (stream.Source == "" || stream.Source == "cdp"):
		item.Source, item.Trust, item.Visibility = "cdp", "browser_reported", "application_messages_and_lifecycle"
	}
	return item
}

func captureSummarizeCollection(report *CaptureCoverageReport, session *store.BrowserSession) {
	coverage := &report.Capture
	if report.Source == "unknown" || (report.Source == "no_capture" && report.Observed.Records != 0) {
		coverage.Unknowns = append(coverage.Unknowns, "capture_source_unknown")
	}
	if session != nil {
		coverage.MetadataPresent, coverage.Finished = true, session.FinishedAt != ""
		coverage.TimedOut, coverage.ErrorReported = session.TimedOut, session.CaptureError != ""
		received := session.ReceivedRequests
		coverage.ReceivedRecords = &received
		if session.ExpectedRequests != nil {
			expected := *session.ExpectedRequests
			coverage.ExpectedRecords = &expected
			if expected < 0 || expected != report.Observed.Records {
				coverage.MetadataInvalid++
			}
		}
		if received < 0 || received != report.Observed.Records || session.DroppedRequests < 0 {
			coverage.MetadataInvalid++
		}
		coverage.DroppedRecords = max(0, int64(session.DroppedRequests))
		coverage.Stats, report.Projection.MetadataDetailsOmitted = captureKnownCounters(session.CaptureStats, []string{
			"retained_body_bytes", "observed_events", "dropped_events", "dropped_requests", "exported_records",
			"protocol_payloads", "webrtc_media", "protocol_observer_reported_contexts", "protocol_observer_destroyed_contexts",
			"protocol_observer_dropped_events", "protocol_observer_observed_bytes", "protocol_observer_captured_bytes",
		}, &coverage.MetadataInvalid)
		var omitted int
		coverage.Limits, omitted = captureKnownCounters(session.CaptureLimits, []string{
			"max_body_bytes", "max_total_body_bytes", "max_requests", "max_events", "max_native_backlog_bytes",
		}, &coverage.MetadataInvalid)
		report.Projection.MetadataDetailsOmitted += omitted
		coverage.DroppedRecords = max(coverage.DroppedRecords, coverage.Stats["dropped_requests"])
		coverage.WarningsTotal = len(session.CaptureWarnings)
		codes := map[string]bool{}
		for _, warning := range session.CaptureWarnings {
			if captureKnownWarning(warning) {
				codes[warning] = true
			} else {
				coverage.WarningsOmitted++
			}
		}
		for code := range codes {
			coverage.WarningCodes = append(coverage.WarningCodes, code)
		}
		sort.Strings(coverage.WarningCodes)
		if len(coverage.WarningCodes) > 12 {
			coverage.WarningsOmitted += len(coverage.WarningCodes) - 12
			coverage.WarningCodes = coverage.WarningCodes[:12]
		}
		report.Projection.MetadataDetailsOmitted += coverage.WarningsOmitted
	} else {
		coverage.Unknowns = append(coverage.Unknowns, "collector_metadata_missing")
	}
	if coverage.ExpectedRecords == nil {
		coverage.Unknowns = append(coverage.Unknowns, "expected_record_count_unknown")
	}
	if !coverage.Finished {
		coverage.Unknowns = append(coverage.Unknowns, "capture_end_unknown")
	}
	if report.ResponseBodies.States["unknown"] > 0 || report.RequestBodies.States["unknown"] > 0 {
		coverage.Unknowns = append(coverage.Unknowns, "body_coverage_unknown")
	}
	if report.Streams.States["unknown"] > 0 {
		coverage.Unknowns = append(coverage.Unknowns, "stream_coverage_unknown")
	}
	if report.Observed.UnknownRecords > 0 {
		coverage.Unknowns = append(coverage.Unknowns, "unsupported_record_kind")
	}
	if report.Projection.MetadataDetailsOmitted > 0 {
		coverage.Unknowns = append(coverage.Unknowns, "capture_metadata_not_interpreted")
	}
	if report.ResponseBodies.CountersSaturated || report.RequestBodies.CountersSaturated || report.Streams.CountersSaturated {
		coverage.Unknowns = append(coverage.Unknowns, "counter_overflow")
	}
	if coverage.MetadataInvalid+report.ResponseBodies.MetadataInvalid+report.RequestBodies.MetadataInvalid+report.Streams.MetadataInvalid > 0 {
		coverage.Unknowns = append(coverage.Unknowns, "inconsistent_metadata")
	}
	if coverage.DroppedRecords > 0 {
		coverage.GapSignals = append(coverage.GapSignals, "dropped_records")
	}
	if coverage.Stats["dropped_events"] > 0 || coverage.Stats["protocol_observer_dropped_events"] > 0 || report.Streams.DroppedEvents > 0 {
		coverage.GapSignals = append(coverage.GapSignals, "dropped_events")
	}
	if coverage.WarningsTotal > 0 {
		coverage.GapSignals = append(coverage.GapSignals, "collector_warnings")
	}
	if coverage.TimedOut || coverage.ErrorReported {
		coverage.GapSignals = append(coverage.GapSignals, "capture_timeout_or_error")
	}
	if report.ResponseBodies.LegacyLossSignals > 0 {
		coverage.GapSignals = append(coverage.GapSignals, "body_loss_flags")
	}
	for _, state := range []string{"partial", "pending", "unavailable"} {
		if report.ResponseBodies.States[state] > 0 || report.RequestBodies.States[state] > 0 {
			coverage.GapSignals = append(coverage.GapSignals, "body_"+state)
		}
		if report.Streams.States[state] > 0 {
			coverage.GapSignals = append(coverage.GapSignals, "stream_"+state)
		}
	}
	if report.Streams.RetainedGapEvents > 0 || report.Streams.TruncatedEvents > 0 {
		coverage.GapSignals = append(coverage.GapSignals, "stream_event_gaps")
	}
	if report.Streams.Open > 0 || report.Observed.UnfinishedHTTP > 0 {
		coverage.GapSignals = append(coverage.GapSignals, "unfinished_records")
	}
	switch {
	case report.Source == "no_capture" && report.Observed.Records == 0:
		coverage.State = "no_capture"
	case len(coverage.GapSignals) > 0:
		coverage.State = "gaps_reported"
	case len(coverage.Unknowns) == 0:
		coverage.State = "no_reported_gaps"
	}
}

func captureKnownCounters(input map[string]int64, keys []string, invalid *int) (map[string]int64, int) {
	result := map[string]int64{}
	for _, key := range keys {
		if value, found := input[key]; found {
			if value >= 0 {
				result[key] = value
			} else {
				(*invalid)++
			}
		}
	}
	return result, len(input) - len(result)
}

func captureKnownWarning(value string) bool {
	switch value {
	case "total_body_limit", "body_limit", "request_limit", "event_limit", "related_targets_unavailable",
		"late_metadata_after_export", "request_metadata_limit", "download_metadata_limit", "late_download_after_export",
		"websocket_identity_conflict", "websocket_event_after_close", "browser_payload_decode_failed",
		"protocol_observer_setup_failed", "protocol_observer_unknown_context", "protocol_observer_context_unavailable",
		"protocol_observer_context_limit", "protocol_event_after_close", "execution_context_destroyed", "target_detached":
		return true
	}
	return false
}

// Saturating arithmetic prevents malformed archive counters from wrapping to
// negative values. These totals are declared lower bounds at MaxInt64.
func captureAdd(total *int64, value int64) bool {
	if value <= 0 {
		return false
	}
	if value > math.MaxInt64-*total {
		*total = math.MaxInt64
		return true
	} else {
		*total += value
	}
	return false
}
