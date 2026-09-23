package browserflow

import (
	"strings"
	"testing"
)

func TestPlanValidatesEntireProgramBeforeEffects(t *testing.T) {
	valid := `{"version":1,"url":"https://example.test/form","steps":[{"id":"name","action":"fill","target":{"name":"Name","role":"textbox"},"value":"private-value"},{"id":"save","action":"click","target":{"name":"Save","role":"button"},"after":[{"target":{"selector":"#status"},"text":"Saved"}]}]}`
	if _, err := Decode(strings.NewReader(valid)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(valid, `"version":1`, `"version":2`, 1),
		strings.Replace(valid, `"url":"https://example.test/form"`, `"url":"javascript:alert(1)"`, 1),
		strings.Replace(valid, `"id":"save"`, `"id":"name"`, 1),
		strings.Replace(valid, `"action":"fill"`, `"action":"eval"`, 1),
		strings.Replace(valid, `"value":"private-value"`, `"script":"private-value"`, 1),
		strings.Replace(valid, `"after":[{"target":{"selector":"#status"},"text":"Saved"}]`, `"after":[]`, 1),
		strings.Replace(valid, `"selector":"#status"`, `"goal":"find status"`, 1),
		strings.Replace(valid, `"target":{"name":"Name","role":"textbox"}`, `"target":{"goal":"name","selector":"input"}`, 1),
		strings.Replace(valid, `"action":"fill"`, `"action":"choose"`, 1),
		valid + ` {}`,
		strings.Repeat(" ", MaxPlanBytes+1),
	} {
		if _, err := Decode(strings.NewReader(bad)); err == nil {
			t.Fatalf("invalid plan accepted: %.100s", bad)
		}
	}
}
