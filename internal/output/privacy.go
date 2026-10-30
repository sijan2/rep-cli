package output

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/repplus/rep-cli/internal/store"
	"golang.org/x/term"
)

func Fingerprint(value string) string {
	if strings.HasPrefix(value, "[REDACTED sha256:") && strings.HasSuffix(value, "]") {
		return value
	}
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("[REDACTED sha256:%x bytes:%d]", digest[:4], len(value))
}

// SensitiveName applies to headers, query parameters, JSON and form fields.
// Normalize punctuation so accessToken, access_token and Access-Token agree.
func SensitiveName(name string) bool {
	name = strings.ToLower(name)
	for _, char := range []string{"-", "_", ".", " "} {
		name = strings.ReplaceAll(name, char, "")
	}
	return strings.Contains(name, "password") || strings.Contains(name, "passwd") ||
		strings.Contains(name, "token") || strings.Contains(name, "secret") ||
		strings.Contains(name, "authorization") || strings.Contains(name, "cookie") ||
		strings.Contains(name, "apikey") || strings.Contains(name, "csrf") || strings.Contains(name, "xsrf") ||
		name == "session" || name == "sessionid" || name == "credential" || name == "credentials" ||
		name == "signature" || name == "sig" || name == "xamzcredential" || name == "xgoogcredential"
}

func RedactHeaders(headers store.HeaderMap) store.HeaderMap {
	if headers == nil {
		return nil
	}
	result := make(store.HeaderMap, len(headers))
	for name, values := range headers {
		result[name] = append([]string(nil), values...)
		for index, value := range values {
			if SensitiveName(name) {
				result[name][index] = Fingerprint(value)
			} else {
				result[name][index] = RedactURL(value)
			}
		}
	}
	return result
}

func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	changed := false
	if u.User != nil {
		u.User = url.User("REDACTED")
		changed = true
	}
	query := u.Query()
	for name, values := range query {
		if SensitiveName(name) {
			changed = true
			for index, value := range values {
				values[index] = Fingerprint(value)
			}
			query[name] = values
		}
	}
	// OAuth-style credential fragments are not transmitted as HTTP query
	// parameters, but can still appear in captured page URLs or initiators.
	prefix, fragment := "", u.Fragment
	if before, after, ok := strings.Cut(fragment, "?"); ok {
		prefix, fragment = before+"?", after
	}
	if fields, err := url.ParseQuery(fragment); err == nil {
		fragmentChanged := false
		for name, values := range fields {
			if !SensitiveName(name) {
				continue
			}
			for index, value := range values {
				values[index] = Fingerprint(value)
			}
			fields[name] = values
			fragmentChanged = true
		}
		if fragmentChanged {
			u.Fragment, u.RawFragment = prefix+fields.Encode(), ""
			changed = true
		}
	}
	if !changed {
		return raw
	}
	u.RawQuery = query.Encode()
	return u.String()
}

func RedactValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for name, item := range value {
			if SensitiveName(name) {
				if text, ok := item.(string); ok {
					result[name] = Fingerprint(text)
				} else {
					data, _ := json.Marshal(item)
					result[name] = Fingerprint(string(data))
				}
			} else {
				result[name] = RedactValue(item)
			}
		}
		return result
	case []any:
		result := make([]any, len(value))
		for index, item := range value {
			result[index] = RedactValue(item)
		}
		return result
	case string:
		return RedactBody(RedactURL(value), "text/plain")
	default:
		return value
	}
}

var credentialLine = regexp.MustCompile(`(?im)^((?:authorization|proxy-authorization|cookie|set-cookie)\s*:\s*)([^\r\n]+)`)

var credentialPair = regexp.MustCompile(`(?i)(["']?[a-z0-9_.-]*(?:password|passwd|token|secret|authorization|cookie|api[-_]?key|csrf|xsrf)[a-z0-9_.-]*["']?\s*[:=]\s*)("(?:\\.|[^"\\\r\n])*(?:"|$)|'(?:\\.|[^'\\\r\n])*(?:'|$)|(?:Bearer|Basic)\s+[^\s&;,<>]+|[^\s&;,<>]+)`)

// RedactBody runs before pointer selection/truncation, so selecting a token
// scalar or cutting through its field cannot remove the redaction context.
func RedactBody(body, contentType string) string {
	lowerType := strings.ToLower(contentType)
	if strings.Contains(lowerType, "event-stream") {
		return redactSSE(body)
	}
	if strings.Contains(lowerType, "ndjson") || strings.Contains(lowerType, "jsonl") {
		lines := strings.SplitAfter(body, "\n")
		changed := false
		for i, line := range lines {
			end := ""
			value := line
			if strings.HasSuffix(value, "\n") {
				value = strings.TrimSuffix(value, "\n")
				end = "\n"
			}
			if strings.HasSuffix(value, "\r") {
				value = strings.TrimSuffix(value, "\r")
				end = "\r" + end
			}
			safe := RedactBody(value, "application/json")
			if safe != value {
				changed = true
				lines[i] = safe + end
			}
		}
		if changed {
			return strings.Join(lines, "")
		}
		return body
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&value) == nil && decoder.Decode(new(any)) == io.EOF {
		redacted, _ := json.Marshal(RedactValue(value))
		// Preserve exact bytes for non-sensitive documents (fidelity and offsets).
		original, _ := json.Marshal(value)
		if !bytes.Equal(original, redacted) {
			return string(redacted)
		}
		return body
	}
	if strings.Contains(strings.ToLower(contentType), "x-www-form-urlencoded") {
		if form, err := url.ParseQuery(body); err == nil {
			changed := false
			for name, values := range form {
				if SensitiveName(name) {
					changed = true
					for i, value := range values {
						values[i] = Fingerprint(value)
					}
					form[name] = values
				}
			}
			if changed {
				return form.Encode()
			}
		}
	}
	// HTTP-style credentials may include spaces (Bearer, Basic, cookie lists).
	body = credentialLine.ReplaceAllStringFunc(body, func(match string) string {
		parts := credentialLine.FindStringSubmatch(match)
		encoded, _ := json.Marshal(Fingerprint(parts[2]))
		return parts[1] + string(encoded)
	})
	// Also cover JSON lines, SSE payloads and textual header-like credentials.
	return credentialPair.ReplaceAllStringFunc(body, func(match string) string {
		parts := credentialPair.FindStringSubmatch(match)
		value := strings.Trim(parts[2], "\"'")
		encoded, _ := json.Marshal(Fingerprint(value))
		return parts[1] + string(encoded)
	})
}

// Marshal returns compact JSON for pipes and indented JSON for terminals.
func Marshal(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if term.IsTerminal(int(os.Stdout.Fd())) {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'}), nil
}

func WriteJSON(writer io.Writer, value any) error {
	data, err := Marshal(value)
	if err != nil {
		return err
	}
	_, err = writer.Write(append(data, '\n'))
	return err
}

// SSE data may span several data: lines. Reconstruct the application payload
// before redacting it, then serialize changed payloads back into valid events.
// Preserve original bytes when no change is necessary.
func redactSSE(body string) string {
	normalized := strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(normalized, "\n")
	changed := false
	flush := func(start, end int) {
		positions := []int{}
		values := []string{}
		for i := start; i < end; i++ {
			if strings.HasPrefix(lines[i], "data:") {
				positions = append(positions, i)
				values = append(values, strings.TrimPrefix(strings.TrimPrefix(lines[i], "data:"), " "))
			}
		}
		if len(positions) == 0 {
			return
		}
		value := strings.Join(values, "\n")
		safe := RedactBody(value, "application/json")
		if safe == value {
			return
		}
		changed = true
		lines[positions[0]] = "data: " + strings.ReplaceAll(safe, "\n", "\ndata: ")
		for _, i := range positions[1:] {
			lines[i] = ":"
		}
	}
	start := 0
	for i, line := range lines {
		if line == "" {
			flush(start, i)
			start = i + 1
		}
	}
	flush(start, len(lines))
	if !changed {
		return body
	}
	return strings.Join(lines, "\n")
}
