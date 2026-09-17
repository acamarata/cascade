package main

import (
	"runtime"
	"testing"

	"github.com/acamarata/cascade/internal/mcp"
	cascaderuntime "github.com/acamarata/cascade/internal/runtime"
)

// TestTheGrantStoreRefusalIsAssertedPerPlatform is Art.5's "any exclusion
// is an asserted refusal, never a silent skip", written as one test that
// runs on every platform and asserts the opposite thing on each.
//
// A t.Skip on Windows would be exactly the silent exclusion Art.5 forbids:
// it would report green while proving nothing, and the day the store
// composition becomes portable nobody would notice this test had stopped
// meaning anything.
func TestTheGrantStoreRefusalIsAssertedPerPlatform(t *testing.T) {
	filter, closeFilter := mcpToolFilter(doctorTestPaths(t), cascaderuntime.SystemClock{})
	if closeFilter != nil {
		t.Cleanup(closeFilter)
	}
	_, denied := filter.(mcp.DenyAllFilter)

	if runtime.GOOS == "windows" {
		if !denied {
			t.Fatal("windows built a real grant-store filter; the store composition is POSIX-only at HEAD, " +
				"so either this refusal is now wrong or a filter was built over a store that does not exist")
		}
		return
	}
	if denied {
		t.Fatalf("%s produced the deny-all filter; the grant store is available on this platform "+
			"and a real policy filter should have been built", runtime.GOOS)
	}
}
