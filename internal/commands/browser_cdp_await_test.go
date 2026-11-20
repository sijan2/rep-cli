package commands

import "testing"

func TestHasTopLevelAwait(t *testing.T) {
	cases := map[string]bool{
		`await fetch("/x")`: true,
		`for (let i=0;i<3;i++){ await new Promise(r=>setTimeout(r,9)) }`: true,
		`(async()=>{ await fetch("/x") })()`:                             false,
		`document.title`:                                                 false,
		`obj.await`:                                                      false,
		`const awaited = 1`:                                              false,
	}
	for expression, want := range cases {
		if got := hasTopLevelAwait(expression); got != want {
			t.Errorf("hasTopLevelAwait(%q) = %v, want %v", expression, got, want)
		}
	}
}
