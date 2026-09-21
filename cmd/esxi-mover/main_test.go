package main

import (
	"strings"
	"testing"
)

func TestAdminTokenIsGeneratedWhenNoneIsSupplied(t *testing.T) {
	a, shownA, e := adminToken("")
	if e != nil || len(a) != 64 || shownA != a {
		t.Fatalf("a generated token must be 64 hex characters and printed: %q %q %v", a, shownA, e)
	}
	if b, _, _ := adminToken(""); a == b {
		t.Fatal("two runs produced the same token")
	}
}

// A supplied token lets a detached container be signed into, but it must be
// strong enough for a UI that takes root credentials, and it is never echoed.
func TestSuppliedAdminTokenIsUsedButNeverPrinted(t *testing.T) {
	supplied := "0123456789abcdef0123456789abcdef"
	token, shown, e := adminToken(supplied)
	if e != nil || token != supplied {
		t.Fatalf("the supplied token was not used: %q %v", token, e)
	}
	if strings.Contains(shown, supplied) {
		t.Fatal("the supplied token would be printed to the container log")
	}
	for _, weak := range []string{"admin", "short-token", "has a space in it 123", "trailing-newline-12345\n"} {
		if _, _, e := adminToken(weak); e == nil {
			t.Fatalf("an unsuitable token was accepted: %q", weak)
		}
	}
}
