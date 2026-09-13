// Purpose: DEFECT-cli-surfaces-promise-embedded-mode.md's regression test
// for `cascade status`: fetchStatus must refuse with an actionable error
// when the daemonless probe confirms no daemon is running, rather than
// building a client and dialing a socket nothing serves. Untagged
// (unlike status_test.go's sibling unit tests): fetchStatus's guard is
// pure Go with no platform-specific behaviour, so this proves it on every
// platform CI runs, windows included.
//
// SPORT: cmd/cascade/status (FIX, daemonless refusal).
package main

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestFetchStatus_RefusesWhenDaemonless is the mutation-proof target:
// removing fetchStatus's guard makes this fail — deps.DialContext is left
// nil on purpose, so if the guard is bypassed the very next line
// (resolveStatusSocket -> client.New(..., client.DialFunc(nil), ...))
// panics on the nil dereference instead of quietly answering wrong. This
// file deliberately imports neither "net" nor "net/http" (Art.7.2's
// no-network unit lane), which is exactly why DialContext is left unset
// rather than given a working stub.
func TestFetchStatus_RefusesWhenDaemonless(t *testing.T) {
	deps := statusDeps{
		Paths:   fakeDaemonPaths{root: t.TempDir()},
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
	}
	ctx := runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: true})

	_, err := fetchStatus(ctx, deps)
	if err == nil {
		t.Fatal("fetchStatus in daemonless mode: want a refusal, got nil")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if !strings.Contains(err.Error(), "cascade daemon start") {
		t.Errorf("err = %v, want it to say how to fix this", err)
	}
}

// The other half of the guard's condition — an undecidable probe result
// (ok=false) does NOT refuse here, since status has no embedded answer to
// fall back to and "unknown" defaults to attempting the dial, mirroring
// approval.go's approvalClient — is already covered by
// TestFetchStatus_DaemonNotRunning (status_test.go, !windows-tagged,
// which is the file that owns newHermeticStatusDeps and the "net"-shaped
// dialer this untagged file must not import).
