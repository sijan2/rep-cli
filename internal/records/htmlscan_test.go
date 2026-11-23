package records

import (
	"fmt"
	"strings"
	"testing"
)

func TestScanHTMLBoundaries(t *testing.T) {
	input := `<!DOCTYPE html><!-- <shreddit-comment> --><TEMPLATE for=x><DIV title="a > b" data-v='&amp;' disabled>hello &lt;x&gt;<br/><script>const x = '</div>'; </script><style>x{content:'<p>'}</style></DIV></TEMPLATE>`
	s := htmlScanner{body: []byte(input)}
	var tags, texts []string
	for token, ok := s.next(); ok; token, ok = s.next() {
		if token.Kind == startTag {
			tags = append(tags, token.Name)
		}
		if token.Kind == textToken {
			texts = append(texts, token.Text)
		}
		if token.Name == "div" && token.Kind == startTag {
			if token.Attrs["title"] != "a > b" || token.Attrs["data-v"] != "&" {
				t.Fatalf("quoted attributes: %+v", token)
			}
			if _, ok := token.Attrs["disabled"]; !ok {
				t.Fatal("boolean attribute lost")
			}
		}
		if token.Offset < 0 || token.End <= token.Offset || token.End > len(input) {
			t.Fatalf("invalid byte offsets: %+v", token)
		}
	}
	if s.err != nil || strings.Join(tags, ",") != "template,div,br,script,style" || strings.Join(texts, "|") != "hello <x>|const x = '</div>'; |x{content:'<p>'}" {
		t.Fatalf("scan: %v %v %v", tags, texts, s.err)
	}
}

func TestScanRejectsTruncation(t *testing.T) {
	for _, input := range []string{`<div a="unfinished`, `<!-- unfinished`, `<script>missing close`, `<` + strings.Repeat("a", 257) + `>`} {
		s := htmlScanner{body: []byte(input)}
		for _, ok := s.next(); ok; _, ok = s.next() {
		}
		if s.err == nil {
			t.Fatalf("accepted malformed tag, length %d", len(input))
		}
	}
}

// The live Reddit thread had a 35 KB script src and 13 KB ad attributes ahead
// of its comments; failing on them lost the whole page.
func TestScanSkipsOversizedAttributes(t *testing.T) {
	var many strings.Builder
	for i := 0; i < maxAttrs; i++ {
		fmt.Fprintf(&many, " a%d", i)
	}
	input := `<script src="data:` + strings.Repeat("x", maxAttrBytes+1) + `" async>if (a < b) {}</script>` +
		`<div` + many.String() + ` extra="dropped">` +
		`<p ` + strings.Repeat("y", 257) + `=1 d="kept">`
	s := htmlScanner{body: []byte(input)}
	var tags []htmlToken
	for token, ok := s.next(); ok; token, ok = s.next() {
		if token.Kind == startTag {
			tags = append(tags, token)
		}
	}
	if s.err != nil || len(tags) != 3 {
		t.Fatalf("scan: %d tags, %v", len(tags), s.err)
	}
	if _, ok := tags[0].Attrs["src"]; ok {
		t.Fatal("oversized value kept")
	}
	if _, ok := tags[0].Attrs["async"]; !ok {
		t.Fatal("attribute after an oversized value lost")
	}
	if _, ok := tags[1].Attrs["extra"]; ok || len(tags[1].Attrs) != maxAttrs {
		t.Fatalf("attribute cap: %d kept", len(tags[1].Attrs))
	}
	if tags[2].Attrs["d"] != "kept" || len(tags[2].Attrs) != 1 {
		t.Fatalf("oversized name: %v", tags[2].Attrs)
	}
}

func TestScanSelfClosingAttributes(t *testing.T) {
	for _, input := range []string{`<DIV value=x disabled/>`, `<DIV value=x/>`, `<DIV value='x'/>`} {
		s := htmlScanner{body: []byte(input)}
		token, ok := s.next()
		if !ok || !token.SelfClosing || token.Attrs["value"] != "x" {
			t.Fatalf("self-closing attributes: %+v %v", token, s.err)
		}
	}
}

func FuzzScan(f *testing.F) {
	for _, seed := range []string{"", "<", `a < b > c`, `<DIV A='>' B=x disabled/>`, `<!-- <p> --><template><p>&amp;</p></template>`, `<script>"</div>"</script>`, `<style>"</stylex>"</style>`, `<p a="`, "\x00\xff<\n", strings.Repeat("<div>", 5000)} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 1<<20 {
			t.Skip()
		}
		s := htmlScanner{body: body}
		previous := 0
		for token, ok := s.next(); ok; token, ok = s.next() {
			if token.Offset < previous || token.End <= token.Offset || token.End > len(body) {
				t.Fatalf("scanner did not advance: %+v", token)
			}
			previous = token.End
		}
		_, _ = DecodeReddit(body, Source{})
	})
}
