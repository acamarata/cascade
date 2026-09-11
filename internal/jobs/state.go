package jobs

// Purpose: JobState, the R-16.37 job lifecycle enum as amended by
//
//	R-21.140, plus the legal-transition table this ticket's contract
//	requires: authorization-shaped, fail-closed, asserted against the
//	spec's own edge list rather than against a second copy of itself.
//
// Inputs: a candidate (from, to) pair, or a raw stored string to decode.
// Outputs: TransitionAllowed's bool + *cascade.Error; DecodeJobState's
//
//	JobState (never a panic, never a permissive default).
//
// Constraints: no permissive zero value -- unknown/unparseable decodes
//
//	to JobStateFailed. The four policy-reserved edges (running->
//	verifying, verifying->reviewing, reviewing->accepted, reviewing->
//	rejected) are refused on the PUBLIC path (TransitionAllowed) with a
//	typed error; only internal/policy may invoke them, and it does so
//	through PolicyTransitionAllowed, a distinct function this ticket
//	ships but S-60.T3 is the one that ever calls with real policy
//	authority (see testonly-allow.json).
//
// SPORT: jobs/job-state-machine/ADD (P1-E29-W6-S59-T1).

import "github.com/acamarata/cascade/pkg/cascade"

// JobState is the closed R-16.37 job lifecycle, amended by R-21.140 to
// add the non-terminal Cancelling. The zero value is deliberately not a
// member.
type JobState string

// The closed JobState vocabulary, in R-16.37/R-21.140 order.
const (
	JobStatePending    JobState = "pending"
	JobStateLeased     JobState = "leased"
	JobStateRunning    JobState = "running"
	JobStateVerifying  JobState = "verifying"
	JobStateReviewing  JobState = "reviewing"
	JobStateAccepted   JobState = "accepted"
	JobStateRejected   JobState = "rejected"
	JobStateCancelling JobState = "cancelling"
	JobStateCancelled  JobState = "cancelled"
	JobStateFailed     JobState = "failed"
)

// Valid reports whether s is one of the ten closed values.
func (s JobState) Valid() bool {
	switch s {
	case JobStatePending, JobStateLeased, JobStateRunning, JobStateVerifying,
		JobStateReviewing, JobStateAccepted, JobStateRejected,
		JobStateCancelling, JobStateCancelled, JobStateFailed:
		return true
	}
	return false
}

// Terminal reports whether s is one of the three states no transition
// leaves (accepted, rejected, cancelled) or the fourth, failed. Cancelling
// is explicitly NOT terminal -- it is the sole gateway into cancelled.
func (s JobState) Terminal() bool {
	switch s {
	case JobStateAccepted, JobStateRejected, JobStateCancelled, JobStateFailed:
		return true
	case JobStatePending, JobStateLeased, JobStateRunning, JobStateVerifying,
		JobStateReviewing, JobStateCancelling:
		return false
	}
	return false
}

// DecodeJobState parses a stored TEXT value into a JobState.
// Unknown/unparseable resolves to JobStateFailed per R-16.37 -- never a
// panic, never a permissive pending/zero-value default.
func DecodeJobState(raw string) JobState {
	s := JobState(raw)
	if !s.Valid() {
		return JobStateFailed
	}
	return s
}

// transitionEdge is one legal (from, to) pair.
type transitionEdge struct {
	from JobState
	to   JobState
}

// publicEdges is the legal-transition table for the PUBLIC store path:
// pending->leased, leased->running, any non-terminal->cancelling,
// cancelling->cancelled, any non-terminal->failed (R-16.37 as amended by
// R-21.140). Built from the spec's own edge list, not copied from a
// second table elsewhere in this package, so a change to one is a change
// to the single source TransitionAllowed and policyEdges both read.
var publicEdges = buildPublicEdges()

// nonTerminalStates lists every JobState for which Terminal() is false,
// used to generate the "any non-terminal -> X" edges without hand-copying
// the state list a second time.
var nonTerminalStates = []JobState{
	JobStatePending, JobStateLeased, JobStateRunning, JobStateVerifying,
	JobStateReviewing, JobStateCancelling,
}

func buildPublicEdges() map[transitionEdge]bool {
	edges := map[transitionEdge]bool{
		{JobStatePending, JobStateLeased}:       true,
		{JobStateLeased, JobStateRunning}:       true,
		{JobStateCancelling, JobStateCancelled}: true,
	}
	for _, from := range nonTerminalStates {
		edges[transitionEdge{from, JobStateCancelling}] = true
		edges[transitionEdge{from, JobStateFailed}] = true
	}
	return edges
}

// policyEdges is the closed set of transitions ONLY internal/policy may
// invoke (running->verifying, verifying->reviewing, reviewing->accepted,
// reviewing->rejected). The public store path refuses every one of
// these with a typed transition error.
var policyEdges = map[transitionEdge]bool{
	{JobStateRunning, JobStateVerifying}:   true,
	{JobStateVerifying, JobStateReviewing}: true,
	{JobStateReviewing, JobStateAccepted}:  true,
	{JobStateReviewing, JobStateRejected}:  true,
}

// TransitionAllowed reports whether the PUBLIC store path may move a job
// from `from` to `to`. It FAILS CLOSED in every direction: an unknown
// from/to state, a policy-reserved edge, and any pair absent from
// publicEdges all return a non-nil *cascade.Error rather than silently
// permitting or silently refusing with no explanation.
func TransitionAllowed(from, to JobState) error {
	if !from.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "jobs: unknown job state %q", string(from))
	}
	if !to.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "jobs: unknown job state %q", string(to))
	}
	if policyEdges[transitionEdge{from, to}] {
		return cascade.Newf(cascade.KindPermissionDenied,
			"jobs: transition %s->%s is policy-reserved; only internal/policy may invoke it", from, to)
	}
	if !publicEdges[transitionEdge{from, to}] {
		return cascade.Newf(cascade.KindConflict, "jobs: illegal transition %s->%s", from, to)
	}
	return nil
}

// PolicyTransitionAllowed reports whether the policy-authorized path may
// move a job from `from` to `to`: exactly the four policyEdges, on top
// of every publicEdges transition (a policy caller may also drive any
// public edge). Reserved for internal/policy's real caller
// (AC/S-60.T3); see testonly-allow.json for why no production caller
// exists yet in this ticket's own files_scope.
func PolicyTransitionAllowed(from, to JobState) error {
	if !from.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "jobs: unknown job state %q", string(from))
	}
	if !to.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "jobs: unknown job state %q", string(to))
	}
	if policyEdges[transitionEdge{from, to}] || publicEdges[transitionEdge{from, to}] {
		return nil
	}
	return cascade.Newf(cascade.KindConflict, "jobs: illegal transition %s->%s", from, to)
}
