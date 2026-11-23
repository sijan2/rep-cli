package records

import (
	"fmt"
	"reflect"
	"testing"
)

func testRecord(id, parent string) Record {
	return Record{ID: id, R: "r_" + id, Parent: parent, Kind: "comment", Author: "u", Time: "2024-01-01T00:00:00Z", Text: "body", Metrics: map[string]int64{"score": 1}}
}

func TestThreadCyclesOrphansAndReadingOrder(t *testing.T) {
	p := newPage("reddit", "thread")
	p.Thread = &Record{ID: "root"}
	wire := 7
	p.Records = []Record{testRecord("a", "root"), testRecord("b", "root"), testRecord("c", "a"), testRecord("cycle1", "cycle2"), testRecord("cycle2", "cycle1"), testRecord("orphan", "missing"), testRecord("self", "self")}
	p.Records[5].WireDepth = &wire
	tree := NewThread(p)
	ordered := tree.Ordered()
	ids := []string{}
	for _, r := range ordered {
		ids = append(ids, r.ID)
	}
	equalFact(t, "DFS", ids, []string{"a", "c", "b", "orphan", "cycle1", "cycle2", "self"})
	for _, id := range []string{"cycle1", "cycle2", "self"} {
		if !hasFlag(tree.Records[tree.byID[id]], "cycle") {
			t.Fatal("cycle not flagged")
		}
	}
	if orphan := tree.Records[tree.byID["orphan"]]; !hasFlag(orphan, "orphan") || orphan.Depth != wire {
		t.Fatal("orphan lost its wire depth")
	}
	next := newPage("reddit", "more_comments")
	next.Records = []Record{testRecord("missing", "root"), testRecord("d", "a")}
	tree.Merge(next, nil)
	if orphan := tree.Records[tree.byID["orphan"]]; hasFlag(orphan, "orphan") || orphan.Depth != 1 {
		t.Fatal("late parent did not repair orphan")
	}
	ids = nil
	for _, r := range tree.Ordered() {
		ids = append(ids, r.ID)
	}
	equalFact(t, "stable siblings", ids[:4], []string{"a", "c", "d", "b"})
}

func TestThreadFirstWinsAndFillsEmptyFields(t *testing.T) {
	p := newPage("reddit", "thread")
	p.Thread = &Record{ID: "root"}
	r := testRecord("a", "root")
	r.Text, r.Author = "", ""
	p.Records = []Record{r}
	tree := NewThread(p)
	next := newPage("reddit", "more_comments")
	next.Records = []Record{testRecord("a", "root")}
	next.Records[0].Metrics["score"] = 9
	next.Records[0].Metrics["extra"] = 2
	next.Records[0].Src = Source{RequestID: "later", SHA256: "digest", Offset: 20}
	delta := tree.Merge(next, nil)
	if delta != (Delta{Updated: 1}) || tree.Records[0].Metrics["score"] != 1 || tree.Records[0].Text != "body" || p.Records[0].Text != "" {
		t.Fatalf("fill: %+v %+v", delta, tree.Records)
	}
	if next := tree.Merge(next, nil); next != (Delta{Duplicates: 1}) {
		t.Fatal("duplicate counted twice")
	}
	if tree.Records[0].Src != next.Records[0].Src {
		t.Fatal("empty provenance not filled")
	}
	copy := tree.Ordered()
	copy[0].Metrics["score"] = 99
	if tree.Records[0].Metrics["score"] != 1 {
		t.Fatal("ordered aliases thread")
	}
}

func TestThreadLongReverseParentChain(t *testing.T) {
	p := newPage("reddit", "thread")
	p.Thread = &Record{ID: "r0"}
	for i := 20000; i > 0; i-- {
		p.Records = append(p.Records, testRecord(fmt.Sprintf("r%d", i), fmt.Sprintf("r%d", i-1)))
	}
	tree := NewThread(p)
	if tree.Records[0].Depth != 19999 || len(tree.Ordered()) != 20000 {
		t.Fatal("deep chain lost records")
	}
	if !reflect.DeepEqual(p.Records[0].Flags, []string(nil)) {
		t.Fatal("input page mutated")
	}
}
