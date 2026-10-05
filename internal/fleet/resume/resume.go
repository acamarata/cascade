// Purpose: ResumeManager's construction, its top-level Run orchestration,
//   and the taxonomy sentinels/report shapes the rest of the package
//   returns through it.
// Inputs: a journal.Store, an injected runtime.Clock, and an optional
//   runtime.EventBus for surfacing terminal/unknown-outcome/attention
//   items.
// Outputs: a Report summarizing every entity the scan touched, or a
//   pkg/cascade taxonomy error if the scan itself could not run at all.
// Constraints: no bare time.Now (Clock only); Run never panics; a Windows
//   GOOS refuses before touching the journal at all (platform_windows.go).
//   Run never dispatches a fan-out: a fan-out cursor is classified only, and
//   its client re-attaches with its request_id (EPIC Decision 12).
// SPORT: internal.fleet.resume.ResumeManager/ADDED (P1-E13-W3-S27-T2).

package resume

import (
	"context"
	goruntime "runtime"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Classification is the fail-closed outcome the scan assigns to one
// entity's journal state.
type Classification uint8

const (
	_ Classification = iota
	// ClassResumable means every fact needed to resume safely is present
	// and recognized.
	ClassResumable
	// ClassTerminal means the cursor cannot be resumed and will never be
	// retried automatically (unrecognized shape, decode failure, or a
	// truncated tail in the acknowledged range).
	ClassTerminal
	// ClassUnknownOutcome means an intent's effect cannot be proven to
	// have happened or not, and its action is not declared idempotent —
	// held for reconciliation, never auto-replayed (R-21.221).
	ClassUnknownOutcome
)

// String renders c for logs and test failure messages.
func (c Classification) String() string {
	switch c {
	case ClassResumable:
		return "resumable"
	case ClassTerminal:
		return "terminal"
	case ClassUnknownOutcome:
		return "unknown-outcome"
	default:
		return "invalid"
	}
}

// AttentionItem is a fact the scan surfaces regardless of any one
// entity's classification — most notably a truncated journal tail, which
// R-21.216 requires is "surfaced as an attention item, never treated as
// no work" even when the surviving prefix has nothing left to resume.
type AttentionItem struct {
	EntityID string
	Reason   string
}

// Outcome is one entity's resume result.
type Outcome struct {
	EntityID       string
	Classification Classification
	// Err carries the typed A-T7 error for Terminal/UnknownOutcome, and a
	// re-queue failure for a Resumable intent whose re-queue append failed.
	Err error
}

// Report summarizes one ResumeManager.Run call.
type Report struct {
	// ColdStart is true when the journal held no entities at all — the
	// explicit, non-error "nothing to do" case (acceptance criterion).
	ColdStart bool
	Outcomes  []Outcome
	Attention []AttentionItem
}

// ErrWindowsUnsupported is Windows tier-2's typed refusal: the daemon
// service this package resumes for does not exist on Windows at all
// (D/S-07.T4's headless one-shot path never runs a resumable daemon).
var ErrWindowsUnsupported = cascade.New(cascade.KindUnsupported, "resume: the cascade daemon (and therefore crash/upgrade resume) does not exist on Windows; use the headless one-shot commands instead")

// ErrConstructionFailed reports a nil required collaborator: New's journal,
// or a FanOutDeps field Scan and Sweep need.
var ErrConstructionFailed = cascade.New(cascade.KindInvalidInput, "resume: construction requires every collaborator to be non-nil")

// ErrTruncatedTail reports that the journal store's own recovery scan
// found and removed a torn tail for an entity — a partial checkpoint,
// terminal per R-21.216, never treated as "no work".
var ErrTruncatedTail = cascade.New(cascade.KindIntegrity, "resume: entity's journal tail was truncated by recovery (partial checkpoint)")

// ErrUnrecognizedShape reports an entity whose journal entries do not
// resolve to either resumable shape this package knows (a fan-out cursor
// or an idempotency-declared intent) — fail-closed, never promoted to
// resumable.
var ErrUnrecognizedShape = cascade.New(cascade.KindInvalidInput, "resume: entity's journal entries do not match a recognized resumable shape")

// ErrAmbiguousOutcome reports an unacknowledged intent whose action is not
// declared idempotent (or whose declaration is absent/unrecognized) — held
// for reconciliation, never auto-replayed.
var ErrAmbiguousOutcome = cascade.New(cascade.KindConflict, "resume: unacknowledged action is not declared idempotent; held for reconciliation")

// Manager is the ResumeManager. The zero value is not usable; build one
// with New.
type Manager struct {
	journal journal.Store
	clock   runtime.Clock
	bus     runtime.EventBus // optional; nil means "no event surfaced"
	goos    string
}

// New builds a Manager. journal is required; clock defaults to the system
// clock and bus is optional. goos selects the platform-refusal check (pass
// runtime.GOOS in production, a literal in tests) — see
// platform_windows.go. The Manager holds no dispatch seam: it classifies
// fan-out cursors and re-queues idempotent intents, nothing else.
func New(journalStore journal.Store, clock runtime.Clock, bus runtime.EventBus, goos string) (*Manager, error) {
	if journalStore == nil {
		return nil, ErrConstructionFailed
	}
	if clock == nil {
		clock = runtime.SystemClock{}
	}
	if goos == "" {
		goos = goruntime.GOOS
	}
	return &Manager{journal: journalStore, clock: clock, bus: bus, goos: goos}, nil
}

// RefuseOnGOOS reports ErrWindowsUnsupported when goos is "windows", nil
// otherwise. Kept here (not in a "_windows.go"-suffixed file) so it
// compiles and is directly unit-testable on every platform: Go's build
// system treats any file whose name ends "_windows.go" as implicitly
// windows-only regardless of an explicit build tag, which would make this
// portable, parameter-driven check invisible on darwin/linux CI —
// discovered while wiring this exact function into platform_windows.go
// (see that file's own doc comment). Manager.Run calls RefuseOnGOOS(m.goos),
// and m.goos defaults to runtime.GOOS in production (New, above), so
// "GOOS=windows go build" still includes this code path (Art.5).
func RefuseOnGOOS(goos string) error {
	if goos == "windows" {
		return ErrWindowsUnsupported
	}
	return nil
}

// Run scans every entity in the journal, classifies it, re-queues every
// resumable idempotent intent, and returns a Report. A resumable fan-out
// cursor is reported Resumable and left untouched: nothing is dispatched
// without a caller, and the client re-attaches with its request_id. On
// Windows it refuses immediately without touching the journal (Art.5
// platform tier-2). An empty journal (no entities at all) returns
// Report{ColdStart: true} and a nil error — not a failure.
func (m *Manager) Run(ctx context.Context) (Report, error) {
	if err := RefuseOnGOOS(m.goos); err != nil {
		return Report{}, err
	}
	entities, err := m.journal.ListEntities(ctx)
	if err != nil {
		return Report{}, cascade.Wrap(cascade.KindUnavailable, err, "resume: listing journal entities")
	}
	if len(entities) == 0 {
		return Report{ColdStart: true}, nil
	}

	report := Report{}
	for _, id := range entities {
		outcome, attention, hasOutcome := m.runOne(ctx, id)
		if hasOutcome {
			report.Outcomes = append(report.Outcomes, outcome)
		}
		if attention != nil {
			report.Attention = append(report.Attention, *attention)
		}
	}
	return report, nil
}

// runOne scans exactly one entity, re-queues it when it is a resumable
// intent (a fan-out cursor is classified only), and publishes a typed A-T7
// event for any non-nil outcome error (never a silent drop, per the
// ticket's acceptance criterion). hasOutcome is
// false only for an entity with nothing open to report at all (fully
// completed, or no recognized cursor present) — kept out of Report.Outcomes
// so a large journal's quiescent entities do not drown the ones that
// actually needed a decision.
func (m *Manager) runOne(ctx context.Context, entityID string) (outcome Outcome, attention *AttentionItem, hasOutcome bool) {
	cursor, attention, err := m.scanEntity(ctx, entityID)
	if err != nil {
		outcome = Outcome{EntityID: entityID, Classification: ClassTerminal, Err: err}
		if cascade.HasKind(err, cascade.KindConflict) {
			outcome.Classification = ClassUnknownOutcome
		}
		m.publish(ctx, outcome)
		return outcome, attention, true
	}
	if cursor == nil {
		// attention is always nil here: scanEntity only sets it alongside
		// a non-nil error (ErrTruncatedTail), already handled above.
		return Outcome{}, nil, false
	}

	submitErr := m.requeue(ctx, *cursor)
	outcome = Outcome{EntityID: entityID, Classification: ClassResumable, Err: submitErr}
	if submitErr != nil {
		m.publish(ctx, outcome)
	}
	return outcome, attention, true
}

// publish surfaces outcome.Err on the event bus, when one is configured,
// as a typed A-T7 error. A nil bus is a valid "no event surfaced"
// configuration, never a panic.
func (m *Manager) publish(ctx context.Context, outcome Outcome) {
	if m.bus == nil || outcome.Err == nil {
		return
	}
	kind, _ := cascade.KindOf(outcome.Err)
	_ = m.bus.Publish(ctx, "daemon", "resume."+outcome.Classification.String(), "resume", []byte(kind.String()+": "+outcome.Err.Error()+" ("+outcome.EntityID+")"))
}
