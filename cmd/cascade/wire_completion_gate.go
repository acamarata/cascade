//go:build !windows

// Purpose: registers the completion-gate daemon wiring: the completion-gate hook pack (see hooks.go's header).
// SPORT: cmd/cascade daemon registrations (P1-CORE-01).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

var _ = registerDaemonWiring(daemonRegistration{
	Name: "completion-gate", Phase: phaseFleet, Order: 5,
	Wire: func(w *daemonWiring) error {
		if w.Deps.CI == nil && w.Deps.Fleet == nil {
			return wireCompletionGateWithoutCI(w)
		}
		verifier, err := newCIAcceptanceVerifier(w)
		if err != nil {
			return err
		}
		w.Deps.Evidence, err = wireCompletionEvidence(w.Ctx, w.Registry, w.Store, w.Clock, w.Bus, w.Paths,
			verifier, w.Deps.CI.Dispatcher(), w.Deps.Fleet.Leases(), completionController(w.Paths))
		return err
	},
})

// wireCompletionGateWithoutCI mounts the gate on a daemon with no fleet
// runtime (wireCI skipped, so no CI runtime can exist). No acceptance re-run
// can run, so every attempt to accept is denied; nothing is accepted by
// default. A daemon that has a fleet but lost its CI runtime still fails.
func wireCompletionGateWithoutCI(w *daemonWiring) error {
	var leases *jobs.LeaseManager
	if w.Deps.Fleet != nil {
		leases = w.Deps.Fleet.Leases()
	}
	var err error
	w.Deps.Evidence, err = wireCompletionEvidence(w.Ctx, w.Registry, w.Store, w.Clock, w.Bus, w.Paths,
		noCIAcceptance{}, noCIAcceptance{}, leases, completionController(w.Paths))
	return err
}

// noCIAcceptance is the acceptance source of a daemon with no CI runtime.
type noCIAcceptance struct{}

// VerifyAcceptance reports the missing runtime as an error, which denies.
func (noCIAcceptance) VerifyAcceptance(context.Context, string) (jobs.AcceptanceVerdict, error) {
	return jobs.AcceptanceVerdict{}, cascade.New(cascade.KindUnavailable, "completion-gate: CI runtime unavailable")
}

// CurrentCheckpoint reports that no checkpoint exists.
func (noCIAcceptance) CurrentCheckpoint(context.Context, string) (ci.CandidateSnapshot, bool, error) {
	return ci.CandidateSnapshot{}, false, nil
}
