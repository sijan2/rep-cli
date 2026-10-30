package store

import "testing"

func TestSemanticDisplayRetainsFullHash(t *testing.T) {
	a := Request{ID: "h_abcd1111111111111111111111111111", Method: "GET"}
	b := Request{ID: "h_abcd2222222222222222222222222222", Method: "GET"}
	first, second := GenerateSemanticID(&a), GenerateSemanticID(&b)
	if first == second || first.Hash() != "abcd1111111111111111111111111111" {
		t.Fatalf("request display truncated canonical identity: %s %s", first, second)
	}
}
