package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/repplus/rep-cli/internal/output"
	"github.com/repplus/rep-cli/internal/store"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type capturedDownloadOptions struct {
	Overwrite  bool
	KeepRange  bool
	Retries    int
	RetryDelay time.Duration
	Timeout    time.Duration
	Validation artifactValidationOptions
}

// artifactValidationOptions is shared by direct and browser-bound download
// paths. Call prepareArtifactValidator before starting a transfer, then validate
// the completed part file before it is published.
type artifactValidationOptions struct {
	AllowEmpty           bool
	AllowHTML            bool
	MinimumBytes         int64
	ExpectedContentTypes []string
	ExpectedMagic        string
}

type artifactValidationResult struct {
	Size                int64
	ContentType         string
	DetectedContentType string
}

type artifactMagic struct {
	label    string
	prefixes [][]byte
}

type artifactValidator struct {
	options              artifactValidationOptions
	expectedContentTypes []string
	expectedMagic        artifactMagic
}

type capturedDownloadResult struct {
	RequestID           string `json:"request_id"`
	OutputPath          string `json:"output_path"`
	Status              int    `json:"status"`
	Bytes               int64  `json:"bytes"`
	SHA256              string `json:"sha256"`
	ContentType         string `json:"content_type"`
	DetectedContentType string `json:"detected_content_type"`
	Retries             int    `json:"retry_limit"`
}

var (
	downloadOverwrite  bool
	downloadKeepRange  bool
	downloadRetries    int
	downloadRetryDelay time.Duration
	downloadTimeout    time.Duration
	downloadValidation artifactValidationOptions
)

var downloadCmd = &cobra.Command{
	Use:   "download <request-id> <output-path>",
	Short: "Replay a captured GET directly into an atomic file",
	Long: `Download from a captured browser request without printing its URL,
cookies, signed query, headers, or response body. rep feeds the request to curl
through stdin, strips HTTP/2 pseudoheaders and Range by default, writes to a
mode-0600 part file, then atomically publishes and hashes the result.

The captured final request should be used directly. Redirect following is
intentionally disabled so explicit Cookie headers cannot leak to another host.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		req := lookupRequestForReplay(args[0])
		if req == nil {
			return output.EmitAgentError(os.Stdout, output.NewAgentError(
				output.ErrCodeRequestNotFound,
				"download",
				fmt.Sprintf("no request matched %q", args[0]),
				"rep list --primary=false --pattern 'download|export|artifact' --limit 10 -o meta",
			), getOutputMode() == "json")
		}
		result, err := downloadCapturedRequest(cmd.Context(), req, args[1], capturedDownloadOptions{
			Overwrite: downloadOverwrite, KeepRange: downloadKeepRange,
			Retries: downloadRetries, RetryDelay: downloadRetryDelay,
			Timeout: downloadTimeout, Validation: downloadValidation,
		})
		if err != nil {
			return output.EmitAgentError(os.Stdout, output.WrapError(
				err, output.ErrCodeTransferFailed, "download",
				"capture the final GET request in Arc and retry its request ID",
				"use rep browser download <request-id> <output-path> when curl is rejected by browser-bound controls",
			), getOutputMode() == "json")
		}
		if getOutputMode() == "json" {
			data, marshalErr := sonic.MarshalIndent(result, "", "  ")
			if marshalErr != nil {
				return marshalErr
			}
			fmt.Println(string(data))
			return nil
		}
		fmt.Printf("saved: %s\nstatus: %d\nbytes: %d\ncontent-type: %s\ndetected-content-type: %s\nsha256: %s\n",
			result.OutputPath, result.Status, result.Bytes, displayContentType(result.ContentType),
			displayContentType(result.DetectedContentType), result.SHA256)
		return nil
	},
}

func downloadCapturedRequest(ctx context.Context, req *store.Request, outputPath string, opts capturedDownloadOptions) (_ capturedDownloadResult, err error) {
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method != "GET" {
		return capturedDownloadResult{}, fmt.Errorf("captured method %s is not a GET; trigger the action first, then download its final GET request", method)
	}
	parsed, err := url.Parse(req.URL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return capturedDownloadResult{}, errors.New("captured request URL must be absolute HTTP(S)")
	}
	if opts.Retries < 0 || opts.Retries > 10 {
		return capturedDownloadResult{}, errors.New("retries must be between 0 and 10")
	}
	if opts.RetryDelay < 0 || opts.RetryDelay > 30*time.Second {
		return capturedDownloadResult{}, errors.New("retry delay must be between 0 and 30s")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 2 * time.Minute
	}
	if opts.Timeout > 30*time.Minute {
		return capturedDownloadResult{}, errors.New("timeout must not exceed 30m")
	}
	validator, err := prepareArtifactValidator(opts.Validation)
	if err != nil {
		return capturedDownloadResult{}, err
	}

	absPath, err := filepath.Abs(outputPath)
	if err != nil {
		return capturedDownloadResult{}, fmt.Errorf("resolve output path: %w", err)
	}
	parent := filepath.Dir(absPath)
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return capturedDownloadResult{}, fmt.Errorf("inspect output directory: %w", err)
	}
	if !parentInfo.IsDir() {
		return capturedDownloadResult{}, fmt.Errorf("output parent is not a directory: %s", parent)
	}
	if !opts.Overwrite {
		if _, statErr := os.Lstat(absPath); statErr == nil {
			return capturedDownloadResult{}, fmt.Errorf("output already exists: %s", absPath)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return capturedDownloadResult{}, fmt.Errorf("inspect output: %w", statErr)
		}
	}

	curlPath, err := exec.LookPath("curl")
	if err != nil {
		return capturedDownloadResult{}, errors.New("curl is not available on PATH")
	}
	part, err := os.CreateTemp(parent, "."+filepath.Base(absPath)+".*.part")
	if err != nil {
		return capturedDownloadResult{}, fmt.Errorf("create part file: %w", err)
	}
	partPath := part.Name()
	published := false
	defer func() {
		_ = part.Close()
		if !published {
			_ = os.Remove(partPath)
		}
	}()
	if err := part.Chmod(0600); err != nil {
		return capturedDownloadResult{}, fmt.Errorf("secure part file: %w", err)
	}
	if err := part.Close(); err != nil {
		return capturedDownloadResult{}, fmt.Errorf("close part file: %w", err)
	}

	headers, err := capturedReplayHeaders(req.Headers, opts.KeepRange)
	if err != nil {
		return capturedDownloadResult{}, err
	}
	config, err := buildCurlStdinConfig(req.URL, headers)
	if err != nil {
		return capturedDownloadResult{}, err
	}
	transferCtx, cancel := context.WithTimeout(ctx, opts.Timeout+5*time.Second)
	defer cancel()
	seconds := int64((opts.Timeout + time.Second - 1) / time.Second)
	delaySeconds := int64((opts.RetryDelay + time.Second - 1) / time.Second)
	args := []string{
		"--disable",
		"--config", "-",
		"--output", partPath,
		"--silent", "--show-error", "--fail-with-body",
		"--proto", "=http,https",
		"--retry", strconv.Itoa(opts.Retries),
		"--retry-delay", strconv.FormatInt(delaySeconds, 10),
		"--retry-all-errors",
		"--max-time", strconv.FormatInt(seconds, 10),
		"--write-out", "%{http_code}\n%{size_download}\n%{content_type}\n",
	}
	command := exec.CommandContext(transferCtx, curlPath, args...)
	command.Stdin = strings.NewReader(config)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if runErr := command.Run(); runErr != nil {
		if errors.Is(transferCtx.Err(), context.DeadlineExceeded) {
			return capturedDownloadResult{}, fmt.Errorf("transfer exceeded %s", opts.Timeout)
		}
		return capturedDownloadResult{}, fmt.Errorf("curl transfer failed: %s", safeTransferError(stderr.String(), req))
	}
	status, reportedBytes, contentType, err := parseCurlWriteOut(stdout.String())
	if err != nil {
		return capturedDownloadResult{}, err
	}
	if status < 200 || status >= 300 {
		if status >= 300 && status < 400 {
			return capturedDownloadResult{}, fmt.Errorf("captured request returned HTTP %d; capture the final redirect request instead", status)
		}
		return capturedDownloadResult{}, fmt.Errorf("captured request returned HTTP %d", status)
	}
	if err := syncFile(partPath); err != nil {
		return capturedDownloadResult{}, err
	}
	info, err := os.Stat(partPath)
	if err != nil {
		return capturedDownloadResult{}, fmt.Errorf("stat completed part: %w", err)
	}
	if info.Size() != reportedBytes {
		return capturedDownloadResult{}, fmt.Errorf("transfer size mismatch: curl=%d file=%d", reportedBytes, info.Size())
	}
	validation, err := validator.validateDownloadedArtifact(partPath, absPath, contentType)
	if err != nil {
		return capturedDownloadResult{}, err
	}
	digest, err := hashFile(partPath)
	if err != nil {
		return capturedDownloadResult{}, err
	}
	if opts.Overwrite {
		if err := os.Rename(partPath, absPath); err != nil {
			return capturedDownloadResult{}, fmt.Errorf("publish output: %w", err)
		}
	} else {
		if err := os.Link(partPath, absPath); err != nil {
			if errors.Is(err, os.ErrExist) {
				return capturedDownloadResult{}, fmt.Errorf("output already exists: %s", absPath)
			}
			return capturedDownloadResult{}, fmt.Errorf("publish output without overwrite: %w", err)
		}
		if err := os.Remove(partPath); err != nil {
			return capturedDownloadResult{}, fmt.Errorf("remove published part link: %w", err)
		}
	}
	published = true
	_ = syncDirectory(parent)
	return capturedDownloadResult{
		RequestID: req.ID, OutputPath: absPath, Status: status,
		Bytes: validation.Size, SHA256: digest, ContentType: validation.ContentType,
		DetectedContentType: validation.DetectedContentType,
		Retries:             opts.Retries,
	}, nil
}

func bindArtifactValidationFlags(flags *pflag.FlagSet, options *artifactValidationOptions) {
	flags.BoolVar(&options.AllowEmpty, "allow-empty", false, "Allow publishing a zero-byte response")
	flags.BoolVar(&options.AllowHTML, "allow-html", false, "Allow an HTML response when the output name or validators imply a binary artifact")
	flags.Int64Var(&options.MinimumBytes, "min-bytes", 0, "Require at least this many response bytes before publishing")
	flags.StringSliceVar(&options.ExpectedContentTypes, "expect-content-type", nil, "Require this response Content-Type (repeatable; type/* allowed)")
	flags.StringVar(&options.ExpectedMagic, "expect-magic", "", "Require a named or hex file prefix (for example zip, pdf, macho, or hex:504b0304)")
}

func prepareArtifactValidator(options artifactValidationOptions) (artifactValidator, error) {
	if options.MinimumBytes < 0 {
		return artifactValidator{}, errors.New("min bytes must not be negative")
	}
	expectedContentTypes, err := normalizeExpectedContentTypes(options.ExpectedContentTypes)
	if err != nil {
		return artifactValidator{}, err
	}
	expectedMagic, err := parseArtifactMagic(options.ExpectedMagic)
	if err != nil {
		return artifactValidator{}, err
	}
	return artifactValidator{
		options:              options,
		expectedContentTypes: expectedContentTypes,
		expectedMagic:        expectedMagic,
	}, nil
}

func (validator artifactValidator) validateDownloadedArtifact(path, intendedOutputPath, declaredContentType string) (artifactValidationResult, error) {
	info, err := os.Stat(path)
	if err != nil {
		return artifactValidationResult{}, fmt.Errorf("inspect completed download: %w", err)
	}
	if !info.Mode().IsRegular() {
		return artifactValidationResult{}, errors.New("download validation failed: completed artifact is not a regular file")
	}
	contentType := sanitizeContentType(declaredContentType)
	prefixLimit := 4096
	for _, prefix := range validator.expectedMagic.prefixes {
		if len(prefix) > prefixLimit {
			prefixLimit = len(prefix)
		}
	}
	prefix, err := readArtifactPrefix(path, prefixLimit)
	if err != nil {
		return artifactValidationResult{}, err
	}
	detectedContentType := ""
	if len(prefix) > 0 {
		detectBytes := prefix
		if len(detectBytes) > 512 {
			detectBytes = detectBytes[:512]
		}
		detectedContentType = sanitizeContentType(http.DetectContentType(detectBytes))
		if hasObviousHTMLPrefix(prefix) && !isHTMLContentType(detectedContentType) {
			detectedContentType = "text/html; charset=utf-8"
		}
	}
	result := artifactValidationResult{
		Size:                info.Size(),
		ContentType:         contentType,
		DetectedContentType: detectedContentType,
	}
	if info.Size() == 0 && !validator.options.AllowEmpty {
		return result, fmt.Errorf("refusing to publish an empty download (declared %s; use --allow-empty if intentional)",
			describeContentType(contentType))
	}
	if len(validator.expectedContentTypes) > 0 && !matchesExpectedContentType(contentType, validator.expectedContentTypes) {
		return result, fmt.Errorf("download validation failed: Content-Type %s did not match expected %s (detected %s, %d bytes)",
			describeContentType(contentType), strings.Join(validator.expectedContentTypes, ", "),
			describeContentType(detectedContentType), info.Size())
	}
	if !validator.options.AllowHTML && validator.expectsNonHTMLArtifact(intendedOutputPath) &&
		(isHTMLContentType(contentType) || isHTMLContentType(detectedContentType) || hasObviousHTMLPrefix(prefix)) {
		return result, fmt.Errorf("download validation failed: refusing an HTML response for binary output %q (declared %s, detected %s, %d bytes); use --allow-html only if intentional",
			strings.ToLower(filepath.Ext(intendedOutputPath)), describeContentType(contentType),
			describeContentType(detectedContentType), info.Size())
	}
	if info.Size() < validator.options.MinimumBytes {
		return result, fmt.Errorf("download validation failed: received %d bytes; minimum is %d (declared %s, detected %s)",
			info.Size(), validator.options.MinimumBytes, describeContentType(contentType),
			describeContentType(detectedContentType))
	}
	if len(validator.expectedMagic.prefixes) > 0 && !matchesArtifactMagic(prefix, validator.expectedMagic) {
		return result, fmt.Errorf("download validation failed: file does not match expected magic %s (declared %s, detected %s, %d bytes)",
			validator.expectedMagic.label, describeContentType(contentType),
			describeContentType(detectedContentType), info.Size())
	}
	return result, nil
}

func normalizeExpectedContentTypes(values []string) ([]string, error) {
	normalized := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		candidate := strings.ToLower(strings.TrimSpace(value))
		if candidate == "" {
			return nil, errors.New("expected content type must not be empty")
		}
		mediaType := ""
		if candidate == "*/*" {
			mediaType = candidate
		} else if strings.HasSuffix(candidate, "/*") && strings.Count(candidate, "/") == 1 {
			major := strings.TrimSuffix(candidate, "/*")
			parsed, _, err := mime.ParseMediaType(major + "/x")
			if err != nil || parsed != major+"/x" {
				return nil, fmt.Errorf("invalid expected content type %q", value)
			}
			mediaType = candidate
		} else {
			parsed, _, err := mime.ParseMediaType(candidate)
			if err != nil || strings.Count(parsed, "/") != 1 || strings.HasPrefix(parsed, "/") ||
				strings.HasSuffix(parsed, "/") || strings.Contains(parsed, "*") {
				return nil, fmt.Errorf("invalid expected content type %q", value)
			}
			mediaType = strings.ToLower(parsed)
		}
		if !seen[mediaType] {
			seen[mediaType] = true
			normalized = append(normalized, mediaType)
		}
	}
	return normalized, nil
}

func matchesExpectedContentType(actual string, expected []string) bool {
	mediaType, _, err := mime.ParseMediaType(actual)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	for _, candidate := range expected {
		if candidate == "*/*" || candidate == mediaType {
			return true
		}
		if strings.HasSuffix(candidate, "/*") && strings.HasPrefix(mediaType, strings.TrimSuffix(candidate, "*")) {
			return true
		}
	}
	return false
}

func parseArtifactMagic(value string) (artifactMagic, error) {
	candidate := strings.ToLower(strings.TrimSpace(value))
	if candidate == "" {
		return artifactMagic{}, nil
	}
	if strings.HasPrefix(candidate, "hex:") {
		raw := strings.TrimSpace(candidate[len("hex:"):])
		if raw == "" || len(raw)%2 != 0 {
			return artifactMagic{}, errors.New("expect-magic hex value must contain a non-empty even number of hexadecimal digits")
		}
		if len(raw) > 8192 {
			return artifactMagic{}, errors.New("expect-magic hex value must not exceed 4096 bytes")
		}
		prefix, err := hex.DecodeString(raw)
		if err != nil {
			return artifactMagic{}, errors.New("expect-magic hex value contains non-hexadecimal digits")
		}
		return artifactMagic{label: fmt.Sprintf("hex prefix (%d bytes)", len(prefix)), prefixes: [][]byte{prefix}}, nil
	}
	prefixes := map[string][][]byte{
		"zip":    {[]byte("PK\x03\x04"), []byte("PK\x05\x06"), []byte("PK\x07\x08")},
		"ipa":    {[]byte("PK\x03\x04"), []byte("PK\x05\x06"), []byte("PK\x07\x08")},
		"apk":    {[]byte("PK\x03\x04"), []byte("PK\x05\x06"), []byte("PK\x07\x08")},
		"gzip":   {{0x1f, 0x8b}},
		"pdf":    {[]byte("%PDF-")},
		"elf":    {{0x7f, 'E', 'L', 'F'}},
		"macho":  {{0xfe, 0xed, 0xfa, 0xce}, {0xce, 0xfa, 0xed, 0xfe}, {0xfe, 0xed, 0xfa, 0xcf}, {0xcf, 0xfa, 0xed, 0xfe}, {0xca, 0xfe, 0xba, 0xbe}, {0xbe, 0xba, 0xfe, 0xca}, {0xca, 0xfe, 0xba, 0xbf}, {0xbf, 0xba, 0xfe, 0xca}},
		"png":    {{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}},
		"jpeg":   {{0xff, 0xd8, 0xff}},
		"xz":     {{0xfd, '7', 'z', 'X', 'Z', 0x00}},
		"bzip2":  {[]byte("BZh")},
		"zstd":   {{0x28, 0xb5, 0x2f, 0xfd}},
		"7z":     {{'7', 'z', 0xbc, 0xaf, 0x27, 0x1c}},
		"rar":    {[]byte("Rar!\x1a\x07\x00"), []byte("Rar!\x1a\x07\x01\x00")},
		"xar":    {[]byte("xar!")},
		"wasm":   {{0x00, 'a', 's', 'm'}},
		"sqlite": {[]byte("SQLite format 3\x00")},
	}
	matched, ok := prefixes[candidate]
	if !ok {
		return artifactMagic{}, fmt.Errorf("unknown expect-magic %q (use zip, ipa, apk, gzip, pdf, elf, macho, png, jpeg, xz, bzip2, zstd, 7z, rar, xar, wasm, sqlite, or hex:<bytes>)", value)
	}
	return artifactMagic{label: candidate, prefixes: matched}, nil
}

func matchesArtifactMagic(prefix []byte, expected artifactMagic) bool {
	for _, candidate := range expected.prefixes {
		if len(prefix) >= len(candidate) && bytes.Equal(prefix[:len(candidate)], candidate) {
			return true
		}
	}
	return false
}

func readArtifactPrefix(path string, limit int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open completed download for validation: %w", err)
	}
	defer file.Close()
	buffer := make([]byte, limit)
	count, err := io.ReadFull(file, buffer)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("read completed download for validation: %w", err)
	}
	return buffer[:count], nil
}

func (validator artifactValidator) expectsNonHTMLArtifact(outputPath string) bool {
	if len(validator.expectedMagic.prefixes) > 0 {
		return true
	}
	if binaryArtifactExtensions[strings.ToLower(filepath.Ext(outputPath))] {
		return true
	}
	for _, expected := range validator.expectedContentTypes {
		if expected == "*/*" || expected == "text/*" || expected == "application/*" ||
			expected == "text/html" || expected == "application/xhtml+xml" {
			return false
		}
	}
	return len(validator.expectedContentTypes) > 0
}

var binaryArtifactExtensions = map[string]bool{
	".7z": true, ".a": true, ".aar": true, ".apk": true, ".bin": true,
	".bz2": true, ".cab": true, ".deb": true, ".dmg": true, ".dll": true,
	".dylib": true, ".elf": true, ".exe": true, ".gif": true, ".gz": true,
	".heic": true, ".img": true, ".ipa": true, ".iso": true, ".jar": true,
	".jpeg": true, ".jpg": true, ".m4a": true, ".mkv": true, ".mov": true,
	".mp3": true, ".mp4": true, ".msi": true, ".o": true, ".otf": true,
	".pdf": true, ".pkg": true, ".png": true, ".rar": true, ".rpm": true,
	".so": true, ".sqlite": true, ".tar": true, ".tgz": true, ".ttf": true,
	".wasm": true, ".wav": true, ".webp": true, ".woff": true, ".woff2": true,
	".xar": true, ".xip": true, ".xz": true, ".zip": true, ".zst": true,
}

func isHTMLContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	return strings.EqualFold(mediaType, "text/html") || strings.EqualFold(mediaType, "application/xhtml+xml")
}

func hasObviousHTMLPrefix(prefix []byte) bool {
	sample := bytes.TrimSpace(prefix)
	sample = bytes.TrimPrefix(sample, []byte{0xef, 0xbb, 0xbf})
	sample = trimHTMLPreamble(sample)
	if len(sample) > 512 {
		sample = sample[:512]
	}
	lower := bytes.ToLower(sample)
	markers := [][]byte{
		[]byte("<!doctype html"), []byte("<html"), []byte("<head"), []byte("<body"),
		[]byte("<title"), []byte("<meta"), []byte("<script"), []byte("<iframe"),
	}
	for _, marker := range markers {
		if bytes.HasPrefix(lower, marker) {
			return true
		}
	}
	return false
}

func trimHTMLPreamble(sample []byte) []byte {
	for {
		sample = bytes.TrimSpace(sample)
		lower := bytes.ToLower(sample)
		switch {
		case bytes.HasPrefix(lower, []byte("<?xml")):
			end := bytes.Index(sample, []byte("?>"))
			if end < 0 {
				return sample
			}
			sample = sample[end+2:]
		case bytes.HasPrefix(sample, []byte("<!--")):
			end := bytes.Index(sample[4:], []byte("-->"))
			if end < 0 {
				return sample
			}
			sample = sample[4+end+3:]
		default:
			return sample
		}
	}
}

func sanitizeContentType(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, character := range value {
		if character >= 0x20 && character <= 0x7e {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('?')
		}
		if builder.Len() >= 256 {
			break
		}
	}
	return strings.TrimSpace(builder.String())
}

func describeContentType(value string) string {
	if value == "" {
		return "(missing)"
	}
	return strconv.Quote(value)
}

func displayContentType(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func capturedReplayHeaders(headers store.HeaderMap, keepRange bool) ([]string, error) {
	skip := map[string]bool{
		"host": true, "content-length": true, "connection": true,
		"accept-encoding": true, "transfer-encoding": true,
	}
	grouped := make(map[string][]string)
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		left, right := strings.ToLower(names[i]), strings.ToLower(names[j])
		if left == right {
			return names[i] < names[j]
		}
		return left < right
	})
	for _, name := range names {
		values := headers[name]
		lower := strings.ToLower(strings.TrimSpace(name))
		if lower == "" || strings.HasPrefix(lower, ":") || skip[lower] || (lower == "range" && !keepRange) {
			continue
		}
		for _, value := range values {
			if strings.ContainsAny(name+value, "\r\n\x00") {
				return nil, fmt.Errorf("captured header %q contains forbidden control characters", name)
			}
			entry := name + ": " + value
			seen := false
			for _, existing := range grouped[lower] {
				parts := strings.SplitN(existing, ":", 2)
				if strings.EqualFold(existing, entry) || (len(parts) == 2 && strings.TrimSpace(parts[1]) == value) {
					seen = true
					break
				}
			}
			if !seen {
				grouped[lower] = append(grouped[lower], entry)
			}
		}
	}
	keys := make([]string, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		values := grouped[key]
		if key == "cookie" || key == "referer" || key == "user-agent" {
			result = append(result, values[len(values)-1])
			continue
		}
		result = append(result, values...)
	}
	return result, nil
}

func buildCurlStdinConfig(rawURL string, headers []string) (string, error) {
	if strings.ContainsAny(rawURL, "\r\n\x00") {
		return "", errors.New("captured URL contains forbidden control characters")
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "url = \"%s\"\n", escapeCurlConfig(rawURL))
	for _, header := range headers {
		if strings.ContainsAny(header, "\r\n\x00") {
			return "", errors.New("captured header contains forbidden control characters")
		}
		fmt.Fprintf(&builder, "header = \"%s\"\n", escapeCurlConfig(header))
	}
	return builder.String(), nil
}

func escapeCurlConfig(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	return strings.ReplaceAll(value, "\"", "\\\"")
}

func parseCurlWriteOut(value string) (int, int64, string, error) {
	lines := strings.Split(strings.TrimSuffix(value, "\n"), "\n")
	if len(lines) < 3 {
		return 0, 0, "", errors.New("curl returned incomplete transfer metadata")
	}
	status, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return 0, 0, "", errors.New("curl returned an invalid HTTP status")
	}
	bytesWritten, err := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
	if err != nil {
		return 0, 0, "", errors.New("curl returned an invalid byte count")
	}
	return status, bytesWritten, strings.TrimSpace(strings.Join(lines[2:], "\n")), nil
}

func safeTransferError(message string, req *store.Request) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "curl exited unsuccessfully"
	}
	message = strings.ReplaceAll(message, req.URL, "<request-url>")
	for name, values := range req.Headers {
		lower := strings.ToLower(name)
		if lower == "cookie" || lower == "authorization" || strings.Contains(lower, "token") || strings.Contains(lower, "key") || lower == ":path" {
			for _, value := range values {
				if value != "" {
					message = strings.ReplaceAll(message, value, "<redacted>")
				}
			}
		}
	}
	if len(message) > 2048 {
		message = message[:2048] + "…"
	}
	return message
}

func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open completed part: %w", err)
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync completed part: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open completed download for hashing: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash completed download: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func init() {
	rootCmd.AddCommand(downloadCmd)
	downloadCmd.Flags().BoolVar(&downloadOverwrite, "overwrite", false, "Atomically replace an existing output")
	downloadCmd.Flags().BoolVar(&downloadKeepRange, "keep-range", false, "Preserve the captured Range header instead of downloading the full file")
	downloadCmd.Flags().IntVar(&downloadRetries, "retries", 3, "Retry transient GET failures, including HTTP 429")
	downloadCmd.Flags().DurationVar(&downloadRetryDelay, "retry-delay", time.Second, "Delay between retries")
	downloadCmd.Flags().DurationVar(&downloadTimeout, "timeout", 2*time.Minute, "Maximum transfer duration")
	bindArtifactValidationFlags(downloadCmd.Flags(), &downloadValidation)
}
