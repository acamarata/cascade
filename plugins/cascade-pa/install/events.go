// Package install implements P1-E24-W5-S50-T4's conversational install
// flow: propose -> confirm -> deterministic install -> resume, wrapping
// the intent resolver (S-50.T3, pkg/plugin.Resolver), the plugin lifecycle
// engine (O/S-32.T4's plugin.add RPC), and the conversation adapter
// (S-43.T2) into one inline install experience cascade-pa triggers from a
// conversation turn.
package install

// Purpose (this file): the typed event vocabulary the flow publishes
//   through the real events bus -- the seam standing in for S-43.T2's
//   conversation adapter (chat.append_turn's CLIENT-LOCAL ECHO), which
//   this package cannot call directly: cascade-pa never imports internal/
//   (R-16.62, R-14.69), so the adapter is reached only through an injected
//   EventBus, exactly as plugin.go's SetConversations and events.go's
//   SetDigestSubscriber already inject their own internal/-backed seams.
// Inputs: values Flow (flow.go) builds from a resolved plugin.Candidate
//   and an Installer outcome.
// Outputs: one EventBus.Publish call per flow phase transition.
// Constraints: every payload is VALUE-FREE -- structured identifiers and
//   pkg/cascade.Kind classifications only, never a raw error message, a
//   checksum, or free-form user text that could carry sensitive content
//   into a synced/scrubbed transcript (S-44.T1's pipeline exists for
//   exactly that risk; an event describes WHAT happened, not the bytes
//   involved). Imports pkg/plugin and pkg/cascade only (Art.10.2); no bare
//   fmt.Errorf/errors.New.
// SPORT: plugins/cascade-pa/install (ADD) -- P1-E24-W5-S50-T4.

import (
	"context"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// EventKind discriminates the five typed events the flow can publish.
type EventKind string

const (
	// EventInstallProposal is the Propose phase's typed turn: emitted
	// before any side effect (CLIENT-LOCAL ECHO, S-43.T2).
	EventInstallProposal EventKind = "install_proposal"
	// EventInstallDeclined reports an explicit or inferred-as-declined
	// operator answer; no side effect precedes or follows it.
	EventInstallDeclined EventKind = "install_declined"
	// EventInstallFailed reports a fail-closed abort; never followed by
	// EventConversationResume for the same run.
	EventInstallFailed EventKind = "install_failed"
	// EventElevationRequired reports that the add path returned
	// AddOutcomeElevationRequired (06 Sec5.14).
	EventElevationRequired EventKind = "elevation_required"
	// EventConversationResume carries the original intent back to the
	// caller once install succeeded or was already satisfied (Sec5.9).
	EventConversationResume EventKind = "conversation.resume"
)

// Proposal is the Propose phase's payload: what would be installed
// and under what permissions, so the operator can decide before anything
// happens.
type Proposal struct {
	Intent      string
	PluginID    string
	Name        string
	Version     string
	Runtime     plugin.RuntimeMode
	Permissions []string
	Source      plugin.CandidateSource
}

// DeclineReason classifies why Declined fired, without carrying the
// raw operator text that produced it.
type DeclineReason string

const (
	// DeclineExplicit is an operator answer read unambiguously as "no".
	DeclineExplicit DeclineReason = "explicit"
	// DeclineAmbiguous is an answer that could not be read as an explicit
	// "yes" -- treated as a refusal, never inferred as consent.
	DeclineAmbiguous DeclineReason = "ambiguous"
	// DeclineNoInput is CASCADE_NO_INPUT=1: the ask-gate was never
	// attempted at all (Sec5.8 automation parity).
	DeclineNoInput DeclineReason = "no_input"
)

// Declined is the Confirm phase's non-install outcome.
type Declined struct {
	PluginID string
	Reason   DeclineReason
}

// Failed reports a fail-closed abort. Reason is the taxonomy Kind
// of the underlying failure, not its message -- value-free per this file's
// header.
type Failed struct {
	PluginID string
	Reason   cascade.Kind
}

// ElevationRequired reports that O/S-32.T4's add path could not proceed
// without a completed local elevation flow (06 Sec5.14): the chat confirm
// alone never satisfies an elevated add.
type ElevationRequired struct {
	PluginID string
	Runtime  plugin.RuntimeMode
}

// ResumeEvent is the Resume phase's payload: the original intent, resolved
// and ready for the caller to continue uninterrupted.
type ResumeEvent struct {
	Intent           string
	PluginID         string
	AlreadyInstalled bool
}

// Event is the single envelope Publish takes. Exactly one payload field is
// populated, selected by Kind -- callers switch on Kind, never probe every
// field for non-nil.
type Event struct {
	Kind      EventKind
	Proposal  *Proposal
	Declined  *Declined
	Failed    *Failed
	Elevation *ElevationRequired
	Resume    *ResumeEvent
	// ThreadID is the originating conversation thread this run's
	// RunRequest named (round-2 rework, T0 decision D4) -- Flow.publish
	// sets it on every event this run produces, so a real EventBus
	// implementation that echoes into a conversation (the CLIENT-LOCAL
	// ECHO, S-43.T2) appends to THIS thread rather than minting a fresh
	// one per event. Empty when the run was not triggered from a known
	// conversation thread (e.g. no live wire-level caller supplies one
	// yet -- RunIntent's own disclosed gap).
	ThreadID string
}

// ConfirmOutcome is the Confirm phase's explicit answer. ConfirmDeclined
// is the zero value on purpose: a ConfirmGate that forgets to set a field
// never silently reads as approval.
type ConfirmOutcome int

const (
	// ConfirmDeclined is an explicit "no" (the zero value).
	ConfirmDeclined ConfirmOutcome = iota
	// ConfirmYes is an explicit, unambiguous "yes".
	ConfirmYes
	// ConfirmAmbiguous is any answer that could not be read as
	// ConfirmYes -- treated identically to ConfirmDeclined.
	ConfirmAmbiguous
)

// ConfirmGate is the L2-risk ask-gate the Confirm phase awaits (06
// Sec5.15): auto-advance (R/S-39.T2, L0/L1 ceiling) must never satisfy
// this call -- enforcement lives in the real S-43.T2 adapter this
// interface stands in for, not in Flow's own logic (flow.go).
type ConfirmGate interface {
	Confirm(ctx context.Context, proposal Proposal) (ConfirmOutcome, error)
}

// EventBus is the seam through which every install-flow event reaches the
// conversation adapter. A composition root injects the real
// internal/conversation-backed implementation via SetEventBus; until then,
// Publish fails closed rather than silently dropping an event a caller
// believes was delivered.
type EventBus interface {
	// Publish delivers evt. A non-nil error means the event was NOT
	// delivered -- Flow treats that as a reason to abort the run rather
	// than proceed on the assumption the operator saw it.
	Publish(ctx context.Context, evt Event) error
}

// errEventBusUnconfigured is returned by the default EventBus. Not an
// Article-1 stub standing in for unfinished work: it is the correct,
// deliberate behavior of a binary that has not called SetEventBus, exactly
// matching plugin.go's unconfigured tools.Dispatcher and cmd's
// unconfiguredClient precedents.
var errEventBusUnconfigured = cascade.New(cascade.KindUnavailable,
	"cascade-pa install: no event bus wired into this session; the composition root "+
		"has not called install.SetEventBus")

type unconfiguredEventBus struct{}

func (unconfiguredEventBus) Publish(context.Context, Event) error {
	return errEventBusUnconfigured
}

// eventBusState guards the package-level EventBus seam so SetEventBus is
// safe under concurrent registration/test use.
var eventBusState struct {
	mu sync.RWMutex
	b  EventBus
}

// SetEventBus injects the real EventBus implementation. Intended to be
// called once by the composition root before any install flow runs; tests
// call it directly to inject a fake, or pass nil to reset to the
// unconfigured default.
func SetEventBus(b EventBus) {
	eventBusState.mu.Lock()
	eventBusState.b = b
	eventBusState.mu.Unlock()
}

// activeEventBus returns the configured EventBus, or unconfiguredEventBus{}
// if SetEventBus has never been called (or was last called with nil).
func activeEventBus() EventBus {
	eventBusState.mu.RLock()
	defer eventBusState.mu.RUnlock()
	if eventBusState.b == nil {
		return unconfiguredEventBus{}
	}
	return eventBusState.b
}
