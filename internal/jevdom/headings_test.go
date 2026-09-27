package jevdom

import (
	"context"
	"testing"
)

func headingNode(id int, name string, level float64, parent string) map[string]any {
	value := node(id, "heading", name, parent)
	value["properties"] = []map[string]any{{"name": "level", "value": map[string]any{"value": level}}}
	return value
}

func relationNames(candidate Candidate, role string) []string {
	var names []string
	for _, relation := range candidate.ContextRelations {
		if relation.Role == role {
			names = append(names, relation.Name)
		}
	}
	return names
}

func candidateNamed(t *testing.T, snapshot Snapshot, name string) Candidate {
	t.Helper()
	var found []Candidate
	for _, candidate := range snapshot.Candidates {
		if candidate.Name == name {
			found = append(found, candidate)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected one %q candidate, found %d", name, len(found))
	}
	return found[0]
}

// A listing-like page: the seller's name sits under "About this seller" while
// other sellers are named under "Similar items". Only document order connects
// a text node to the heading that labels its section.
func listingPage() fakePage {
	nodes := []map[string]any{
		node(1, "RootWebArea", "Apple iPhone 13 | Listing", ""),
		headingNode(2, "Apple iPhone 13 128GB", 1, "1"),
		node(3, "StaticText", "US $389.99", "1"),
		node(4, "generic", "", "1"),
		headingNode(5, "About this seller", 2, "4"),
		node(6, "generic", "", "4"),
		node(7, "StaticText", "tech_deals_42", "6"),
		node(8, "StaticText", "Joined Mar 2015", "6"),
		headingNode(9, "Contact", 3, "4"),
		node(10, "button", "Contact seller", "4"),
		headingNode(11, "Similar items", 2, "1"),
		node(12, "listitem", "", "11"),
		node(13, "StaticText", "Seller: renewed_direct", "1"),
		node(14, "region", "Seller card", "1"),
		node(15, "link", "Visit store", "14"),
	}
	// Chromium supplies ordered childIds; the response order here is shuffled to
	// prove that document order comes from childIds.
	children := map[string][]string{"1": {"2", "3", "4", "11", "13", "14"}, "4": {"5", "6", "9", "10"}, "6": {"7", "8"}, "14": {"15"}, "11": {"12"}}
	for _, value := range nodes {
		if ids, ok := children[value["nodeId"].(string)]; ok {
			value["childIds"] = ids
		}
	}
	shuffled := append([]map[string]any{nodes[0]}, nodes[len(nodes)-1])
	shuffled = append(shuffled, nodes[1:len(nodes)-1]...)
	return fakePage{frames: []fakeFrame{{id: "frame-main", loader: "loader-main", url: "https://listing.example/item", nodes: shuffled}}}
}

func TestSectionHeadingsLabelTextByDocumentOrder(t *testing.T) {
	snapshot, err := Capture(context.Background(), browserFor(listingPage()), 12, "all", "")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"US $389.99":             "Apple iPhone 13 128GB",
		"tech_deals_42":          "About this seller",
		"Joined Mar 2015":        "About this seller",
		"Contact seller":         "Contact",
		"Seller: renewed_direct": "Similar items",
		"Visit store":            "Similar items",
		"About this seller":      "Apple iPhone 13 128GB", // a heading's enclosing section
		"Contact":                "About this seller",
		"Similar items":          "Apple iPhone 13 128GB", // a new h2 closes the h3 section
	}
	for name, heading := range cases {
		got := relationNames(candidateNamed(t, snapshot, name), "heading")
		if len(got) != 1 || got[0] != heading {
			t.Fatalf("%q headings=%v want %q", name, got, heading)
		}
	}
	if got := relationNames(candidateNamed(t, snapshot, "Apple iPhone 13 128GB"), "heading"); len(got) != 0 {
		t.Fatalf("top heading has an invented section: %v", got)
	}
}

func TestTopFrameTitleIsNotCandidateContextButFramesKeepTheirs(t *testing.T) {
	value := listingPage()
	value.frames = append(value.frames, fakeFrame{id: "frame-payment", loader: "loader-payment", url: "https://listing.example/pay", nodes: []map[string]any{
		node(100, "RootWebArea", "Secure checkout", ""), node(101, "button", "Pay now", "100"),
	}})
	snapshot, err := Capture(context.Background(), browserFor(value), 12, "controls", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := candidateNamed(t, snapshot, "Contact seller").Context; got != "" {
		t.Fatalf("top-frame title used as context: %q", got)
	}
	if got := candidateNamed(t, snapshot, "Pay now").Context; got != "Secure checkout" {
		t.Fatalf("child-frame document lost its distinguishing title: %q", got)
	}
	if got := candidateNamed(t, snapshot, "Visit store").Context; got != "Seller card" {
		t.Fatalf("named ancestor context was lost: %q", got)
	}
}

func TestSectionHeadingChangesInvalidateTheFingerprint(t *testing.T) {
	before, after := listingPage(), listingPage()
	for _, value := range after.frames[0].nodes {
		if value["nodeId"] == "5" {
			value["name"] = map[string]any{"value": "About another seller"}
		}
	}
	a, err := Capture(context.Background(), browserFor(before), 12, "text", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Capture(context.Background(), browserFor(after), 12, "text", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Fingerprint == b.Fingerprint {
		t.Fatal("a changed section heading did not change the observation fingerprint")
	}
}
