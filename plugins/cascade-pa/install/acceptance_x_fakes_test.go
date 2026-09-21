package install_test

// Purpose (this file): the two collaborators the ticket's own prose
//   explicitly allows to be simulated rather than real -- ConfirmGate
//   ("confirm simulated") and the EventBus recorder every acceptance
//   test inspects -- plus acceptWithheldElevator, standing in for "no
//   local elevation broker is configured at all" (R-14.72's literal
//   "with the broker withheld" case). Every OTHER collaborator in this
//   suite (Resolver, Installer, the satisfied-broker Elevator) is real
//   production code; see acceptance_x_installer_test.go and
//   acceptance_x_elevation_test.go.
// SPORT: plugins/cascade-pa/install:acceptance (ADD) -- P1-E24-W5-S50-T7.

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// acceptConfirm simulates the operator's chat confirmation, matching
// this directory's own flow_test.go fakeConfirm pattern (a distinct type
// here: flow_test.go's fakeConfirm is unexported to "package install"
// and unreachable from this external test package).
type acceptConfirm struct {
	outcome install.ConfirmOutcome
	calls   int
}

func (f *acceptConfirm) Confirm(context.Context, install.Proposal) (install.ConfirmOutcome, error) {
	f.calls++
	return f.outcome, nil
}

// acceptBus records every published event in order.
type acceptBus struct {
	events []install.Event
}

func (b *acceptBus) Publish(_ context.Context, evt install.Event) error {
	b.events = append(b.events, evt)
	return nil
}

func (b *acceptBus) has(kind install.EventKind) bool {
	for _, e := range b.events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

// acceptWithheldElevator is install.Elevator with no broker behind it at
// all: every call refuses, fail-closed, exactly the "broker withheld"
// premise R-14.72's acceptance criterion names. It never returns
// Approved:true under any input.
type acceptWithheldElevator struct{}

func (acceptWithheldElevator) Elevate(context.Context, install.ElevationRequest) (install.ElevationResult, error) {
	return install.ElevationResult{}, cascade.New(cascade.KindUnavailable,
		"acceptance: no local elevation broker is configured for this run")
}
