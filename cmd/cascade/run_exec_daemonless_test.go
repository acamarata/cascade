// Purpose: DEFECT-cli-surfaces-promise-embedded-mode.md's regression test
// for `cascade run`: fetchRun must refuse with an actionable error when
// the daemonless probe confirms no daemon is running, rather than
// building a client and dialing a socket nothing serves. This file
// deliberately imports neither "net" nor "net/http" (Art.7.2's
// no-network unit lane): the refusal returns before resolveRunSocket or
// any client is built, so no real transport is ever needed to prove it.
//
// SPORT: cmd/cascade/run (FIX, daemonless refusal).
package main

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestFetchRun_RefusesWhenDaemonless is the mutation-proof target:
// reverting fetchRun's guard makes this fail by instead returning
// runDispatchDeps' malformed-config-toml error (KindInvalidInput), since
// that deps value's config.toml is deliberately unparseable and would be
// the very next thing fetchRun reaches without the guard.
func TestFetchRun_RefusesWhenDaemonless(t *testing.T) {
	ctx := runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: true})

	resp, err := fetchRun(ctx, runDispatchDeps(t), runRequestParams{TaskClass: "chat"})
	if err == nil {
		t.Fatal("fetchRun in daemonless mode: want a refusal, got nil")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if !strings.Contains(err.Error(), "cascade daemon start") {
		t.Errorf("err = %v, want it to say how to fix this", err)
	}
	if resp.JobID != "" || resp.Output != "" {
		t.Errorf("fetchRun returned a populated response alongside its refusal: %+v", resp)
	}
}
