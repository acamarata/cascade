package executive

// Purpose: the worker-to-Conductor escalation ladder. A worker raises an
// Escalation; the ladder tries the repo-context rung, then the
// investigator rung, and journals one exec.ESCALATION per rung tried.
//
// Inputs: an Escalation (the raising job, its kind, a summary, options).
// Outputs: an Outcome naming the rung that resolved it, the owner (only
// for authority escalations) or refused.
//
// Constraints: a worker never addresses the owner: only the authority
// kind routes to the owner rung, and nothing on an Escalation names the
// owner or a target. Every journaled payload passes ValidatePayload and
// GuardSerialized before it is published on ExecNamespace.

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// EscalationKind is the closed reason a worker escalates.
type EscalationKind string

// The closed EscalationKind values.
const (
	// EscalationAmbiguity means the task admits more than one reading.
	EscalationAmbiguity EscalationKind = "ambiguity"
	// EscalationConflict means two inputs disagree.
	EscalationConflict EscalationKind = "conflict"
	// EscalationArchitectural means the task needs a design decision.
	EscalationArchitectural EscalationKind = "architectural"
	// EscalationAuthority means the step needs the owner's authority.
	EscalationAuthority EscalationKind = "authority"
	// EscalationMissingInfo means a fact the task needs is absent.
	EscalationMissingInfo EscalationKind = "missing_info"
)

// validEscalationKinds is the closed EscalationKind set.
var validEscalationKinds = map[EscalationKind]bool{
	EscalationAmbiguity: true, EscalationConflict: true, EscalationArchitectural: true,
	EscalationAuthority: true, EscalationMissingInfo: true,
}

// Resolution names the ladder rung an escalation ended on.
type Resolution string

// The closed Resolution values.
const (
	// ResolutionRepoContext means repository context answered it.
	ResolutionRepoContext Resolution = "repo_context"
	// ResolutionInvestigator means the investigator answered it.
	ResolutionInvestigator Resolution = "investigator"
	// ResolutionOwner means it went to the owner (authority only).
	ResolutionOwner Resolution = "owner"
	// ResolutionRefused means no rung answered it.
	ResolutionRefused Resolution = "refused"
)

// The journaled outcome of one rung.
const (
	outcomeResolved   = "resolved"
	outcomeUnresolved = "unresolved"
)

// Escalation is what a worker raises. It names no owner and no target.
type Escalation struct {
	// FromJob names the job that raised the escalation.
	FromJob string
	// Kind is the reason for the escalation.
	Kind EscalationKind
	// Summary states the question.
	Summary string
	// Options lists the choices the worker sees, if any.
	Options []string
}

// EscalationPayload is the v1 body of exec.ESCALATION.
type EscalationPayload struct {
	// FromJob names the job that raised the escalation.
	FromJob string `json:"from_job"`
	// Kind is the escalation reason.
	Kind EscalationKind `json:"kind"`
	// Summary states the question.
	Summary string `json:"summary"`
	// Step is the rung this event records.
	Step Resolution `json:"step"`
	// Outcome is resolved or unresolved.
	Outcome string `json:"outcome"`
}

// Rung is one step of the ladder. ok reports whether it resolved e, and
// resolvedBy names who did.
type Rung interface {
	Try(ctx context.Context, e Escalation) (resolvedBy string, ok bool, err error)
}

// Outcome is the result of Resolve.
type Outcome struct {
	// Resolution is the rung the escalation ended on.
	Resolution Resolution
	// ResolvedBy names who resolved it; empty unless a rung resolved it.
	ResolvedBy string
	// Blocked reports a refused missing_info escalation: the job cannot go on.
	Blocked bool
}

// Ladder resolves escalations and journals each rung on the bus.
type Ladder struct {
	bus          *events.Bus
	det          *secrets.Detector
	repo         Rung
	investigator Rung
}

// NewLadder returns a Ladder. Every argument is required; a nil one is
// cascade.KindInvalidInput. A kind table that does not place
// exec.ESCALATION in GroupEscalation is cascade.KindInternal: the ladder
// journals nothing it cannot classify.
func NewLadder(bus *events.Bus, det *secrets.Detector, repo, investigator Rung) (*Ladder, error) {
	if bus == nil || det == nil || repo == nil || investigator == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "executive: escalation ladder needs a bus, a detector and both rungs")
	}
	if g, err := GroupOf(KindEscalation); err != nil || g != GroupEscalation {
		return nil, cascade.New(cascade.KindInternal, "executive: the kind table does not register exec.ESCALATION in the escalation group")
	}
	return &Ladder{bus: bus, det: det, repo: repo, investigator: investigator}, nil
}

// Resolve walks the ladder for e. authority goes to the owner rung; every
// other kind tries repo context, then the investigator, and is refused
// when neither resolves it. A rung error is cascade.KindUnavailable and
// stops the walk.
func (l *Ladder) Resolve(ctx context.Context, e Escalation) (Outcome, error) {
	if e.FromJob == "" || e.Summary == "" || !validEscalationKinds[e.Kind] {
		return Outcome{}, cascade.New(cascade.KindInvalidInput, "executive: escalation needs a job, a summary and a known kind")
	}
	if _, err := l.payload(e, ResolutionRefused, outcomeUnresolved); err != nil {
		return Outcome{}, cascade.Wrapf(cascade.KindInvalidInput, err, "executive: escalation payload refused")
	}
	if e.Kind == EscalationAuthority {
		if err := l.journal(ctx, e, ResolutionOwner, outcomeUnresolved); err != nil {
			return Outcome{}, err
		}
		return Outcome{Resolution: ResolutionOwner}, nil
	}
	rungs := []struct {
		step Resolution
		rung Rung
	}{{ResolutionRepoContext, l.repo}, {ResolutionInvestigator, l.investigator}}
	for _, r := range rungs {
		by, ok, err := r.rung.Try(ctx, e)
		if err != nil {
			return Outcome{}, cascade.Wrapf(cascade.KindUnavailable, err, "executive: escalation rung %s failed", r.step)
		}
		outcome := outcomeUnresolved
		if ok {
			outcome = outcomeResolved
		}
		if err := l.journal(ctx, e, r.step, outcome); err != nil {
			return Outcome{}, err
		}
		if ok {
			return Outcome{Resolution: r.step, ResolvedBy: by}, nil
		}
	}
	if err := l.journal(ctx, e, ResolutionRefused, outcomeUnresolved); err != nil {
		return Outcome{}, err
	}
	return Outcome{Resolution: ResolutionRefused, Blocked: e.Kind == EscalationMissingInfo}, nil
}

// payload marshals one rung record and runs ValidatePayload and
// GuardSerialized on it.
func (l *Ladder) payload(e Escalation, step Resolution, outcome string) ([]byte, error) {
	raw, err := json.Marshal(EscalationPayload{FromJob: e.FromJob, Kind: e.Kind, Summary: e.Summary, Step: step, Outcome: outcome})
	if err != nil {
		return nil, err
	}
	if err := ValidatePayload(KindEscalation, PayloadV1, raw); err != nil {
		return nil, err
	}
	if err := GuardSerialized(l.det, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// journal publishes one exec.ESCALATION for step on the job's stream.
func (l *Ladder) journal(ctx context.Context, e Escalation, step Resolution, outcome string) error {
	raw, err := l.payload(e, step, outcome)
	if err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err, "executive: escalation payload refused")
	}
	stream, err := DeriveStreamID(RunID(e.FromJob), "")
	if err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err, "executive: escalation stream")
	}
	_, err = l.bus.PublishEvent(ctx, ExecNamespace, events.Event{
		Kind:           events.EventKind(KindEscalation),
		Payload:        raw,
		PayloadVersion: uint32(PayloadV1),
		RunID:          e.FromJob,
		StreamID:       string(stream),
		Producer:       escalationProducer,
	})
	return err
}
