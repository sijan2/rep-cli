package records

import (
	"bytes"
	"fmt"
	"html"
	"strings"
)

// Real pages carry tags and attribute values far larger than any record field,
// such as data: script sources and ad payloads. The scanner skips values over
// these limits instead of failing the page; no decoder reads such values.
const (
	maxAttrBytes = 16 << 10
	maxAttrs     = 256
	maxHTMLDepth = 4096
)

type tokenKind uint8

const (
	startTag tokenKind = iota
	endTag
	textToken
	commentToken
	declarationToken
)

type htmlToken struct {
	Kind        tokenKind
	Name        string
	Attrs       map[string]string
	Text        string
	Offset, End int
	SelfClosing bool
}

type htmlScanner struct {
	body []byte
	pos  int
	raw  string
	err  error
}

func asciiLower(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

func space(b byte) bool  { return b == ' ' || b == '\t' || b == '\r' || b == '\n' || b == '\f' }
func letter(b byte) bool { return asciiLower(b) >= 'a' && asciiLower(b) <= 'z' }

func (s *htmlScanner) fail(message string) (htmlToken, bool) {
	s.err = fmt.Errorf("HTML at byte %d: %s", s.pos, message)
	return htmlToken{}, false
}

// This is a tokenizer, not an HTML tree repairer. In particular, templates are
// ordinary containers and script/style contents cannot introduce elements.
func (s *htmlScanner) next() (htmlToken, bool) {
	if s.err != nil || s.pos >= len(s.body) {
		return htmlToken{}, false
	}
	begin := s.pos
	if s.raw != "" {
		end := begin
		for end < len(s.body) {
			if s.body[end] == '<' && end+2+len(s.raw) <= len(s.body) && s.body[end+1] == '/' && bytes.EqualFold(s.body[end+2:end+2+len(s.raw)], []byte(s.raw)) {
				after := end + 2 + len(s.raw)
				if after < len(s.body) && (space(s.body[after]) || s.body[after] == '>') {
					break
				}
			}
			end++
		}
		if end == len(s.body) {
			return s.fail("unterminated raw text element")
		}
		s.raw = ""
		if end > begin {
			s.pos = end
			return htmlToken{Kind: textToken, Text: string(s.body[begin:end]), Offset: begin, End: end}, true
		}
	}
	if bytes.HasPrefix(s.body[begin:], []byte("<!--")) {
		end := bytes.Index(s.body[begin+4:], []byte("-->"))
		if end < 0 {
			return s.fail("unterminated comment")
		}
		s.pos = begin + 4 + end + 3
		return htmlToken{Kind: commentToken, Text: string(s.body[begin+4 : s.pos-3]), Offset: begin, End: s.pos}, true
	}
	if s.body[begin] != '<' || begin+1 == len(s.body) || !(letter(s.body[begin+1]) || s.body[begin+1] == '/' || s.body[begin+1] == '!' || s.body[begin+1] == '?') {
		s.pos++
		for s.pos < len(s.body) && s.body[s.pos] != '<' {
			s.pos++
		}
		return htmlToken{Kind: textToken, Text: html.UnescapeString(string(s.body[begin:s.pos])), Offset: begin, End: s.pos}, true
	}
	end, quote := begin+1, byte(0)
	for ; end < len(s.body); end++ {
		b := s.body[end]
		if quote != 0 {
			if b == quote {
				quote = 0
			}
		} else if b == '\'' || b == '"' {
			quote = b
		} else if b == '>' {
			break
		}
	}
	if end == len(s.body) {
		return s.fail("unterminated tag")
	}
	s.pos = end + 1
	t := htmlToken{Kind: startTag, Offset: begin, End: s.pos}
	i := begin + 1
	if s.body[i] == '!' || s.body[i] == '?' {
		t.Kind = declarationToken
		return t, true
	}
	if s.body[i] == '/' {
		t.Kind = endTag
		i++
	}
	name := i
	for i < end && !space(s.body[i]) && s.body[i] != '/' {
		i++
	}
	if i == name || i-name > 256 {
		return s.fail("invalid tag name")
	}
	t.Name = strings.ToLower(string(s.body[name:i]))
	if t.Kind == endTag {
		return t, true
	}
	t.Attrs = map[string]string{}
	count := 0
	for i < end {
		for i < end && space(s.body[i]) {
			i++
		}
		if i == end {
			break
		}
		if s.body[i] == '/' && i+1 == end {
			t.SelfClosing = true
			break
		}
		start := i
		for i < end && !space(s.body[i]) && s.body[i] != '=' && !(s.body[i] == '/' && i+1 == end) {
			i++
		}
		if i == start {
			return s.fail("invalid attribute name")
		}
		skip, key := i-start > 256, ""
		if !skip {
			key = strings.ToLower(string(s.body[start:i]))
		}
		for i < end && space(s.body[i]) {
			i++
		}
		value := ""
		if i < end && s.body[i] == '=' {
			i++
			for i < end && space(s.body[i]) {
				i++
			}
			quote := byte(0)
			if i < end && (s.body[i] == '\'' || s.body[i] == '"') {
				quote = s.body[i]
				i++
			}
			start = i
			for i < end && ((quote != 0 && s.body[i] != quote) || (quote == 0 && !space(s.body[i]) && !(s.body[i] == '/' && i+1 == end))) {
				i++
			}
			skip = skip || i-start > maxAttrBytes
			if !skip {
				value = html.UnescapeString(string(s.body[start:i]))
			}
			if quote != 0 {
				i++
			}
		}
		count++
		if skip || count > maxAttrs {
			continue
		}
		if _, exists := t.Attrs[key]; !exists {
			t.Attrs[key] = value
		}
	}
	if !t.SelfClosing && (t.Name == "script" || t.Name == "style") {
		s.raw = t.Name
	}
	return t, true
}

func voidTag(name string) bool {
	switch name {
	case "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr":
		return true
	}
	return false
}
