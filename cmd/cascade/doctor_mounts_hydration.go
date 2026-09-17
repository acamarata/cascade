// Purpose: the hydration doctor check's store opener, split out of
//
//	doctor_mounts.go under its own 300-line cap (Art.10.3).
//
// SPORT: cmd/cascade/doctor (ADD) — P1-E16-W4-S34-T4.
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"time"

	"github.com/acamarata/cascade/internal/client"
	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/context/hydration"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

// hydrationCountFor reports degraded hydrations the way every other verb
// reaches daemon-owned state: through the daemon when one is live, and
// directly otherwise.
//
// The split is forced, not preferred. `cascade doctor` runs its checks
// CONCURRENTLY and the store driver takes an exclusive lock, so a check
// that opened the database for itself would fail every sibling check that
// wanted the same file — and would fail outright whenever the daemon held
// it, which is the normal state. Asking the owner is the only reading
// that works in both.
func hydrationCountFor(paths runtime.PathProvider, clock runtime.Clock) hydration.CountFunc {
	return func(ctx context.Context) (int, error) {
		if paths == nil {
			return 0, cascade.New(cascade.KindUnavailable,
				"cascade doctor: no data directory is resolved for the hydration event log")
		}
		if count, ok := hydrationCountViaDaemon(ctx, paths); ok {
			return count, nil
		}
		dbPath := filepath.Join(paths.DataDir(), "cascade.db")
		if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
			// A database that does not exist yet is an EMPTY log, not an
			// unreadable one. A fresh install has never published a
			// degraded hydration, and reporting that as an error would
			// make every first run fail its own doctor — the same reading
			// the retrieval-index check gives an index nobody has built.
			return 0, nil
		}
		store, err := sqlite.Open(ctx, dbPath)
		if err != nil {
			// The store driver is single-owner by design. A held lock
			// means something else in this installation has the log
			// open — a running daemon, or a sibling doctor check that
			// got there first — which is a statement about the machine,
			// not a fault, and the check renders it as such.
			if kind, ok := cascade.KindOf(err); ok && kind == cascade.KindConflict {
				return 0, cascade.Wrap(cascade.KindConflict, hydration.ErrLogBusy, err.Error())
			}
			return 0, err
		}
		defer func() { _ = store.Close() }()
		return hydration.CountDegraded(ctx, store, clock.Now(), hydration.DegradedWindow)
	}
}

// hydrationCountViaDaemon asks a live daemon, reporting whether it
// answered. Every failure reports false so the caller falls back to
// reading the log itself.
func hydrationCountViaDaemon(ctx context.Context, paths runtime.PathProvider) (int, bool) {
	settings, err := daemon.ResolveSettings(nil, paths)
	if err != nil {
		return 0, false
	}
	c := client.New(settings.SocketPath, client.DialFunc(client.UnixDialer), hydrationReportTimeout)
	var result daemon.ContextHydrationReportResult
	if derr := c.Do(ctx, daemon.ContextHydrationReportMethod,
		daemon.ContextHydrationReportParams{}, &result); derr != nil {
		return 0, false
	}
	return result.Count, true
}

// hydrationReportTimeout bounds the daemon round trip. A doctor check has
// its own deadline; this one is shorter so a wedged socket falls back to
// the direct read rather than consuming the whole check budget.
const hydrationReportTimeout = 2 * time.Second

// productionHarnessDetector builds the real harness detector: this host's
// GOOS, the real environment, and one stat per candidate root.
func productionHarnessDetector() cascadecontext.HarnessDetector {
	return cascadecontext.NewPathDetector(goruntime.GOOS, os.Getenv, func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}).WithFileReader(os.ReadFile)
}

// productionHarnessDrift reports instruction drift for the invoking
// process's working directory.
//
// A cwd that cannot be resolved yields an error the check treats as "no
// drift information", which leaves the drift columns unset — their
// honest meaning when nothing measured them. Detection still answers,
// because "is it installed" and "is what we generate for it current" are
// different questions and only the second one needs a working directory.
func productionHarnessDrift() cascadecontext.DriftSource {
	return func(ctx context.Context) (cascadecontext.SyncResult, error) {
		cwd, err := os.Getwd()
		if err != nil {
			return cascadecontext.SyncResult{}, err
		}
		return cascadecontext.Sync(ctx, cwd, os.UserHomeDir, true)
	}
}
