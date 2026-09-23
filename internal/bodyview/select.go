// Package bodyview projects captured response bytes without confusing transport
// chunks with application records. Selection never fetches a response again.
package bodyview

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

type Options struct {
	Format       string
	ContentType  string
	Pointer      string
	HasPointer   bool
	RecordOffset int
	Records      int
	Find         string
	Complete     bool
}

type Selection struct {
	Data            []byte `json:"-"`
	Format          string `json:"format"`
	Records         int    `json:"records,omitempty"`
	NextRecord      int    `json:"next_record,omitempty"`
	More            bool   `json:"more_records,omitempty"`
	PendingBytes    int    `json:"pending_bytes,omitempty"`
	SourceValidated bool   `json:"source_validated"`
}

func Select(data []byte, options Options) (Selection, error) {
	if options.RecordOffset < 0 || options.Records < 1 {
		return Selection{}, fmt.Errorf("record offset must be nonnegative and records must be positive")
	}
	format := strings.ToLower(options.Format)
	if options.HasPointer {
		format = "json"
	}
	if options.Find != "" {
		format = "search"
	}
	if format == "auto" {
		switch content := strings.ToLower(options.ContentType); {
		case strings.Contains(content, "text/event-stream"):
			format = "sse"
		case strings.Contains(content, "ndjson"), strings.Contains(content, "jsonl"):
			format = "ndjson"
		case strings.Contains(content, "json"):
			format = "json"
		default:
			format = "raw"
		}
	}
	if (format == "json" || format == "ndjson" || format == "sse") && !utf8.Valid(data) {
		return Selection{}, fmt.Errorf("structured parsing requires complete UTF-8; inspect raw bytes for an incomplete codepoint or other character encoding")
	}
	switch format {
	case "", "raw":
		return Selection{Data: data, Format: "raw"}, nil
	case "json":
		selected, err := selectJSON(data, options.Pointer)
		return Selection{Data: selected, Format: "json", SourceValidated: err == nil}, err
	case "ndjson":
		return selectNDJSON(data, options)
	case "sse":
		return selectSSE(data, options)
	case "search":
		return selectMatches(data, options), nil
	default:
		return Selection{}, fmt.Errorf("format must be raw, auto, json, ndjson, or sse")
	}
}

// Walk tokens to skip unrelated subtrees rather than materializing the whole
// document as a map. Validate the entire document, including trailing content.
func selectJSON(data []byte, pointer string) ([]byte, error) {
	var path []string
	if pointer != "" {
		if !strings.HasPrefix(pointer, "/") {
			return nil, fmt.Errorf("JSON pointer must be empty or begin with /")
		}
		for _, part := range strings.Split(pointer[1:], "/") {
			var key strings.Builder
			for i := 0; i < len(part); i++ {
				if part[i] != '~' {
					key.WriteByte(part[i])
					continue
				}
				if i+1 >= len(part) || (part[i+1] != '0' && part[i+1] != '1') {
					return nil, fmt.Errorf("invalid JSON pointer escape")
				}
				i++
				if part[i] == '0' {
					key.WriteByte('~')
				} else {
					key.WriteByte('/')
				}
			}
			path = append(path, key.String())
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var selected json.RawMessage
	var walk func([]string, bool, int) error
	walk = func(remaining []string, match bool, depth int) error {
		if depth > 512 {
			return fmt.Errorf("JSON nesting exceeds 512 levels")
		}
		if match && len(remaining) == 0 {
			if selected != nil {
				return fmt.Errorf("JSON pointer is ambiguous because an object key is repeated")
			}
			return decoder.Decode(&selected)
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				nextMatch := match && len(remaining) > 0 && key == remaining[0]
				next := remaining
				if nextMatch {
					next = remaining[1:]
				}
				if err := walk(next, nextMatch, depth+1); err != nil {
					return err
				}
			}
		case '[':
			for index := 0; decoder.More(); index++ {
				nextMatch := match && len(remaining) > 0 && remaining[0] == strconv.Itoa(index)
				next := remaining
				if nextMatch {
					next = remaining[1:]
				}
				if err := walk(next, nextMatch, depth+1); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter")
		}
		_, err = decoder.Token()
		return err
	}
	if err := walk(path, true, 0); err != nil {
		return nil, fmt.Errorf("invalid or incomplete JSON at byte %d: %w", decoder.InputOffset(), err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing JSON at byte %d", decoder.InputOffset())
	}
	if selected == nil {
		return nil, fmt.Errorf("JSON pointer was not found")
	}
	return selected, nil
}

type line struct {
	data       []byte
	start, end int
	terminated bool
}

// SSE accepts CRLF, LF, and CR. NDJSON accepts LF and CRLF.
func nextLine(data []byte, start int, sse bool) line {
	for end := start; end < len(data); end++ {
		if data[end] == '\n' || (sse && data[end] == '\r') {
			next := end + 1
			if data[end] == '\r' && next < len(data) && data[next] == '\n' {
				next++
			}
			return line{bytes.TrimSuffix(data[start:end], []byte{'\r'}), start, next, true}
		}
	}
	return line{data[start:], start, len(data), false}
}

type jsonRecord struct {
	Index int             `json:"index"`
	Start int             `json:"byte_start"`
	End   int             `json:"byte_end"`
	Value json.RawMessage `json:"value"`
}

func selectNDJSON(data []byte, options Options) (Selection, error) {
	result := Selection{Format: "ndjson", SourceValidated: true}
	records := []jsonRecord{}
	index := 0
	for offset := 0; offset < len(data); {
		current := nextLine(data, offset, false)
		offset = current.end
		value := bytes.TrimSpace(current.data)
		if len(value) == 0 {
			continue
		}
		if !current.terminated && !options.Complete {
			result.PendingBytes = len(data) - current.start
			result.SourceValidated = false
			break
		}
		if !json.Valid(value) {
			return result, fmt.Errorf("invalid NDJSON record %d at byte %d", index, current.start)
		}
		if index >= options.RecordOffset {
			if len(records) >= options.Records {
				result.More = true
				break
			}
			records = append(records, jsonRecord{index, current.start, current.end, json.RawMessage(value)})
		}
		index++
	}
	result.Records = len(records)
	result.NextRecord = options.RecordOffset + len(records)
	if result.More {
		result.SourceValidated = false
	}
	result.Data, _ = json.Marshal(records)
	return result, nil
}

type eventRecord struct {
	Index int    `json:"index"`
	Start int    `json:"byte_start"`
	End   int    `json:"byte_end"`
	Event string `json:"event"`
	ID    string `json:"id,omitempty"`
	Data  string `json:"data"`
}

func selectSSE(data []byte, options Options) (Selection, error) {
	result := Selection{Format: "sse"}
	records := []eventRecord{}
	index, start := 0, 0
	event, id := "message", ""
	values := []string{}
	for offset := 0; offset < len(data); {
		current := nextLine(data, offset, true)
		offset = current.end
		if !current.terminated {
			result.PendingBytes = len(data) - start
			break
		}
		value := string(current.data)
		if current.start == 0 {
			value = strings.TrimPrefix(value, "\ufeff")
		}
		if value == "" {
			if len(values) > 0 {
				if index >= options.RecordOffset {
					if len(records) >= options.Records {
						result.More = true
						break
					}
					records = append(records, eventRecord{index, start, current.end, event, id, strings.Join(values, "\n")})
				}
				index++
			}
			values = nil
			event = "message"
			start = current.end
			continue
		}
		if strings.HasPrefix(value, ":") {
			continue
		}
		field, content, _ := strings.Cut(value, ":")
		content = strings.TrimPrefix(content, " ")
		switch field {
		case "data":
			values = append(values, content)
		case "event":
			if content != "" {
				event = content
			} else {
				event = "message"
			}
		case "id":
			if !strings.ContainsRune(content, 0) {
				id = content
			}
		}
		if offset == len(data) && start < offset {
			result.PendingBytes = offset - start
		}
	}
	result.Records = len(records)
	result.NextRecord = options.RecordOffset + len(records)
	result.Data, _ = json.Marshal(records)
	return result, nil
}

type textMatch struct {
	Index   int    `json:"index"`
	Offset  int    `json:"byte_offset"`
	Context string `json:"context"`
}

func selectMatches(data []byte, options Options) Selection {
	result := Selection{Format: "search"}
	matches := []textMatch{}
	needle := []byte(options.Find)
	for offset, index := 0, 0; offset <= len(data)-len(needle); index++ {
		at := bytes.Index(data[offset:], needle)
		if at < 0 {
			break
		}
		at += offset
		if index >= options.RecordOffset {
			if len(matches) >= options.Records {
				result.More = true
				break
			}
			matches = append(matches, textMatch{index, at, string(bytes.ToValidUTF8(data[max(0, at-80):min(len(data), at+len(needle)+80)], []byte("�")))})
		}
		offset = at + len(needle)
	}
	result.Records = len(matches)
	result.NextRecord = options.RecordOffset + len(matches)
	result.Data, _ = json.Marshal(matches)
	return result
}
