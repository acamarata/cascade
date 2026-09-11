// Purpose: the daemon's production wiring for internal/fleet/resume
//
//	(M/S-27.T2) — split out of daemon.go under R-14.117's authorized-split
//	allowance (Art.10.3's 300-line-per-file cap; daemon.go was already at
//	297 lines before this ticket). Portable (no build tag): unix's
//	platformDaemonRun (daemon_unix.go) calls wireResumeScan after opening
//	the real store, and windows's platformDaemonRun (daemon_windows.go)
//	calls resumeRefuseOnWindows before doing anything else, so both
//	platform halves reach this package's real code (daemon_windows.go's
//	own call site is written directly there, since it must carry that
//	file's build tag).
//
// Inputs: a real provider.Store and runtime.Clock (unix path only).
// Outputs: a resume.Report, or a taxonomy error surfaced up through
//
//	platformDaemonRun exactly like any other startup failure.
//
// Constraints: DISCLOSED GAP (see wireResumeScan's own doc comment): no
//
//	conductor.Executor is constructed anywhere in this tree's production
//	code yet (conductor.NewExecutor has no call site outside its own
//	package and its own tests) — a separate, larger composition-root gap
//	this ticket does not close. unavailableFanOut is therefore a real,
//	typed KindUnavailable refusal for any resumable fan-out cursor this
//	scan finds, not a stub pretending success (Art.1): classification,
//	cold-start, terminal and unknown-outcome reporting all still run for
//	real against the real journal.
//
// SPORT: cmd/cascade/daemon (ADD, per T-2 sport_updates).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/resume"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// wireResumeScan runs the M/S-27.T2 crash/upgrade-in-place resume scan
// against the real journal domain, over the SAME store platformDaemonRun
// already opened, before the daemon starts serving RPC. A nil bus is
// valid (resume.Report's Attention/Outcomes still populate); this
// composition root does not yet adapt internal/events.Bus's typed
// EventKind Publish onto runtime.EventBus's string-kind one for a second
// subsystem beyond upgrade.go's own (that adapter, runtimeEventBusAdapter,
// lives in the unix-only daemon_unix_run.go and this file must stay
// portable for the windows half below, so it is not reused here).
func wireResumeScan(ctx context.Context, store provider.Store, clock runtime.Clock) (resume.Report, error) {
	js := journal.New(store, clock, journal.DefaultNamespace)
	mgr, err := resume.New(js, unavailableFanOut, nil, nil, clock, nil, "")
	if err != nil {
		return resume.Report{}, err
	}
	return mgr.Run(ctx)
}

// unavailableFanOut is resume's FanOutFunc seam until a real
// conductor.Executor is wired at this composition root. See this file's
// doc comment for why that is a disclosed, real gap rather than
// something this ticket could close on its own.
func unavailableFanOut(context.Context, provider.ModelRequest, int, map[int]conductor.JobID, conductor.WithPermitFn, conductor.JournalAppender) ([]provider.ModelResponse, error) {
	return nil, cascade.New(cascade.KindUnavailable, "resume: no production conductor.Executor is wired at this composition root yet")
}
