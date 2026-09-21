package main

import (
	"strings"
	"testing"
)

func TestAdminToken(t *testing.T) {
	a, shown := adminToken("")
	if len(a) != 64 || shown != a {
		t.Fatalf("a generated token must be 64 hex characters and printed: %q %q", a, shown)
	}
	if b, _ := adminToken(""); a == b {
		t.Fatal("two runs produced the same token")
	}
	token, shown := adminToken("chosen-by-the-operator")
	if token != "chosen-by-the-operator" || strings.Contains(shown, token) {
		t.Fatalf("a supplied token must be used and never printed: %q %q", token, shown)
	}
}
