//go:build !windows

package main

// Purpose (this file): the daemon's conductor accounting carries a reserver
//   that admits, so a real daemon dispatches as it did before calls were
//   reserved instead of refusing every call as unreserved.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestWireConductorAccountingBindsAnAdmittingReserver(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	paths, err := runtime.NewPathProvider(func(string) string { return "" }, func() (string, error) { return dir, nil })
	if err != nil {
		t.Fatalf("NewPathProvider: %v", err)
	}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	acc, err := wireConductorAccounting(ctx, paths, runtime.NewFixedClock(time.Unix(1_700_000_000, 0)),
		newTestProviderRegistry(t))
	if err != nil {
		t.Fatalf("wireConductorAccounting: %v", err)
	}
	if acc.Reserver == nil {
		t.Fatal("ConductorAccounting.Reserver is nil; every daemon dispatch would refuse as unreserved")
	}
	held, err := acc.Reserver.Reserve(ctx, provider.Selection{}, provider.ModelRequest{})
	if err != nil || held == nil {
		t.Fatalf("Reserve = (%v, %v), want an admitted hold", held, err)
	}
	ran := false
	if err := held.WithPermit(ctx, func(context.Context) error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("WithPermit ran=%v err=%v, want the call run", ran, err)
	}
	if err := held.Release(ctx, provider.Usage{}, false); err != nil {
		t.Fatalf("Release: %v", err)
	}
}
