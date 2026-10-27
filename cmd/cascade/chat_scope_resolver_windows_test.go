//go:build windows

// Purpose: the Windows chat scope resolver is absent, so an owner thread there
// is created unscoped (P1-CPA-06).
// SPORT: cmd/cascade chat scope resolver (TEST) -- P1-CPA-06.
package main

import "testing"

// TestChatScopeResolverNilOnWindows: Windows wires no project resolver.
func TestChatScopeResolverNilOnWindows(t *testing.T) {
	if chatScopeResolver(nil) != nil {
		t.Fatal("chatScopeResolver on windows returned a resolver, want nil")
	}
}
