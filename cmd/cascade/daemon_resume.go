//go:build !windows

// Purpose: the daemon's generic journal resume scan. composeDaemon runs
//
//	wireResumeScan right after the store opens and before the socket
//	serves: it re-queues idempotent intents and only classifies fan-out
//	cursors, dispatching nothing. Fan-out cursors are scanned against the
//	conductor's claim table and swept by the fanout-resume registration
//	(wire_resume.go). Crash recovery of a fan-out is the client re-attach
//	(`cascade run --resume <request_id>`), never a background re-dispatch
//	(EPIC Decision 12).
//
// Inputs: the daemon store and clock.
// Outputs: a resume.Report.
// Constraints: Windows has no daemon (daemon_windows.go), so this file is
//
//	unix-only.
//
// SPORT: cmd/cascade/daemon (CHANGE, P1-CORE-15).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/resume"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// wireResumeScan runs the M/S-27.T2 crash/upgrade-in-place resume scan
// against the real journal domain, over the SAME store platformDaemonRun
// already opened, before the daemon starts serving RPC. It re-queues
// idempotent intents and classifies fan-out cursors; it holds no dispatch
// seam. A nil bus is valid (resume.Report's Attention/Outcomes still
// populate).
func wireResumeScan(ctx context.Context, store provider.Store, clock runtime.Clock) (resume.Report, error) {
	js := journal.New(store, clock, journal.DefaultNamespace)
	mgr, err := resume.New(js, clock, nil, "")
	if err != nil {
		return resume.Report{}, err
	}
	return mgr.Run(ctx)
}
