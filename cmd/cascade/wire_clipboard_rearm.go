//go:build !windows

// Purpose: registers the daemon's clipboard re-arm: at start, every pending
//
//	clipboard clear a killed CLI left is cleared (due) or scheduled
//	(remaining), through the daemon's one audit log.
//
// Inputs: the daemon wiring's store, clock and paths.
// Outputs: the "clipboard-rearm" registration.
// Constraints: the effect log is openEffectLog's view of the daemon log
//
//	openDaemonAuditLog opened; no audit.New. A corrupt pending file or an
//	unreadable effect log refuses the start (fail closed).
//
// SPORT: cmd/cascade daemon registrations — clipboard re-arm (P1-SEC-09).
package main

import (
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Order 40 follows the other phaseLate registrations.
var _ = registerDaemonWiring(daemonRegistration{Name: "clipboard-rearm", Phase: phaseLate, Order: 40, Wire: wireClipboardRearm})

// rearmPendingClears is the re-arm entry point. A test substitutes a
// recorder so no test reaches the real clipboard.
var rearmPendingClears = secrets.RearmHostPendingClears

func wireClipboardRearm(w *daemonWiring) error {
	path, err := pendingClearsPath(w.Paths)
	if err != nil {
		return err
	}
	if _, err := openDaemonAuditLog(w.Store, w.Clock, nil, w.Paths); err != nil { // memoised; openEffectLog reads it
		return err
	}
	effects, err := openEffectLog(w.Store, w.Clock, nil, w.Paths)
	if err != nil {
		return err
	}
	// w.Ctx is the daemon's lifetime: it bounds the scheduled clears, so none
	// runs against the effect log after the daemon stopped.
	if err := rearmPendingClears(w.Ctx, w.Clock, effects, secrets.NewFilePendingClearStore(path), secrets.NewSystemClipboardScheduler()); err != nil {
		kind, ok := cascade.KindOf(err)
		if !ok {
			kind = cascade.KindUnavailable
		}
		return cascade.Wrap(kind, err, "clipboard re-arm: pending clipboard clears could not be re-armed")
	}
	return nil
}
