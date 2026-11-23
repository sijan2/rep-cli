package records

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode"
)

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func facts(t testing.TB, name string) map[string]any {
	t.Helper()
	var all map[string]map[string]any
	if err := json.Unmarshal(fixture(t, "facts.json"), &all); err != nil {
		// _about is a string rather than an object.
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(fixture(t, "facts.json"), &raw); err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(raw[name], &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	return all[name]
}

func equalFact(t *testing.T, name string, got, want any) {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var gv, wv any
	_ = json.Unmarshal(g, &gv)
	_ = json.Unmarshal(w, &wv)
	if !reflect.DeepEqual(gv, wv) {
		t.Errorf("%s: got %s; want %s", name, g, w)
	}
}

func nonspace(text string) int {
	count := 0
	for _, r := range text {
		if !unicode.IsSpace(r) {
			count++
		}
	}
	return count
}

type fixtureNode struct {
	token    htmlToken
	parent   *fixtureNode
	children []*fixtureNode
	text     string
}

func fixtureHTML(t *testing.T, body []byte) []*fixtureNode {
	t.Helper()
	root := &fixtureNode{}
	stack := []*fixtureNode{root}
	var nodes []*fixtureNode
	s := htmlScanner{body: body}
	for token, ok := s.next(); ok; token, ok = s.next() {
		switch token.Kind {
		case startTag:
			parent := stack[len(stack)-1]
			n := &fixtureNode{token: token, parent: parent}
			parent.children = append(parent.children, n)
			nodes = append(nodes, n)
			if !voidTag(token.Name) && !token.SelfClosing {
				stack = append(stack, n)
			}
		case endTag:
			for i := len(stack) - 1; i > 0; i-- {
				if stack[i].token.Name == token.Name {
					stack = stack[:i]
					break
				}
			}
		case textToken:
			for _, n := range stack {
				n.text += token.Text
			}
		}
	}
	if s.err != nil {
		t.Fatal(s.err)
	}
	return nodes
}

func (n *fixtureNode) ancestor(name string) *fixtureNode {
	for p := n.parent; p != nil; p = p.parent {
		if p.token.Name == name {
			return p
		}
	}
	return nil
}

func (n *fixtureNode) descendants(name string) []*fixtureNode {
	var found []*fixtureNode
	var walk func(*fixtureNode)
	walk = func(p *fixtureNode) {
		for _, child := range p.children {
			if child.token.Name == name {
				found = append(found, child)
			}
			walk(child)
		}
	}
	walk(n)
	return found
}

func queryKeys(query string) []string {
	keys := []string{}
	for _, pair := range strings.Split(query, "&") {
		if pair != "" {
			keys = append(keys, strings.SplitN(pair, "=", 2)[0])
		}
	}
	return keys
}
