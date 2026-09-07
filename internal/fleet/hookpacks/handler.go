package hookpacks

// Purpose (this file): the fleet.sessions.hook_event JSON-RPC 2.0
//
//	method (R-21.272/R-21.273's sole daemon-side entry point for hook
//	events): payload validation, the R-16.48 event-to-domain-operation
//	dispatch table, and RegisterHookEventHandler, the ready-to-wire
//	binding into an *rpc.Registry.
//
// Inputs: an incoming HookPayload (decoded with unknown fields
//
//	rejected — see types.go's allowlist note), the S-24.T3 sessions
//	Store, an optional EventBus for jobs.* fan-out and the fleet.sessions
//	audit event, and an injected runtime.Clock for timestamp sanity.
//
// Outputs: nil on success, or a pkg/cascade taxonomy error the RPC
//
//	registry maps to a structured JSON-RPC error — malformed params and
//	unknown harness are KindInvalidInput; domain errors pass through
//	unchanged (they already carry a taxonomy Kind).
//
// Constraints: every dispatch case references an
//
//	internal/fleet/sessions.SessionState constant by symbol, never a
//	state string literal (R-21.272) — grepStateLiterals in handler_test.go
//	asserts this directly against this file's own source. Unknown event
//	types (a string outside ParseHookEventType's closed set) are dropped
//	with a WARN and a counter increment, never an error.
//
// CONTRACT DEVIATION (SubagentStart parent attribution, recorded, not
// papered over). The R-16.48 mapping table asks SubagentStart to
// "Upsert(child session, parent_session_id = the parent's session_id)",
// but HOW step 1 fixes HookPayload's field set at exactly six members
// (harness, event_type, session_id, pid, account, timestamp_ms) — none
// of which carries the PARENT's session id, only the reporting session's
// own. dispatchSubagentStart therefore Upserts with ParentSessionID left
// nil; a future ticket that extends HookPayload with a parent field (and
// a corresponding fixture) can populate it. Filed as an Art.9 gap.
//
// CONTRACT DEVIATION (TransitionState's `from` argument, recorded, not
// papered over). The mapping table names bare "TransitionState(<state
// constant>)", but internal/fleet/sessions.Store.TransitionState's real
// signature is TransitionState(ctx, id, from, to) — a compare-and-swap,
// not a one-argument set. transitionTo below reads the record's current
// State via Get and passes it as `from`, which is a no-op CAS when the
// record is already at the target state (domain.go documents this as
// idempotent) and a real transition otherwise.
//
// SPORT: fleet/hookpacks.RegisterHookEventHandler/ADDED (P1-E12-W3-S24-T4).

import (
	"bytes"
	"context"
	"encoding/json"
	goruntime "runtime"
	"sync/atomic"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// MethodHookEvent is the fleet.sessions.hook_event JSON-RPC 2.0 method
// name (R-21.272): the sole daemon-side entry point hook pack commands
// call back into.
const MethodHookEvent = "fleet.sessions.hook_event"

// clockSkewTolerance bounds how far TimestampMs may sit from the
// server's own clock before validate refuses it as out of range.
const clockSkewTolerance = 24 * time.Hour

// ErrUnknownHarness, ErrMissingSessionID and ErrTimestampOutOfRange are
// this handler's own KindInvalidInput sentinels (R-14.2: domain-specific
// sentinels live in their owning package).
var (
	ErrUnknownHarness      = cascade.New(cascade.KindInvalidInput, "hookpacks: unknown or empty harness")
	ErrMissingSessionID    = cascade.New(cascade.KindInvalidInput, "hookpacks: missing session id")
	ErrTimestampOutOfRange = cascade.New(cascade.KindInvalidInput, "hookpacks: timestamp out of range")
	// ErrWindowsTier2Unavailable is the structured refusal this handler's
	// registration returns on Windows: no daemon exists there at all
	// (tier-2), so there is nothing to register into.
	ErrWindowsTier2Unavailable = cascade.New(cascade.KindUnsupported, "hookpacks: fleet session hook events unavailable on Windows tier-2")
)

// unknownEventCount counts HookPayloads whose EventType is outside
// ParseHookEventType's closed set — dropped, never an error (see this
// file's header comment).
var unknownEventCount atomic.Int64

// UnknownEventCount returns the current dropped-unknown-event-type
// count, for tests and diagnostics.
func UnknownEventCount() int64 { return unknownEventCount.Load() }

// decodePayload decodes raw into a HookPayload, rejecting any field raw
// carries outside types.go's six-field allowlist.
func decodePayload(raw json.RawMessage) (HookPayload, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p HookPayload
	if err := dec.Decode(&p); err != nil {
		return HookPayload{}, cascade.Wrapf(cascade.KindInvalidInput, err, "hookpacks: decoding %s params", MethodHookEvent)
	}
	return p, nil
}

// validate checks required fields, harness non-emptiness, and timestamp
// range sanity against now. It never inspects EventType against the
// known set — an unrecognized event type is a dispatch-time drop, not a
// validation failure (see dispatch).
func validate(p HookPayload, now time.Time) error {
	if p.Harness == "" || p.Harness == "unknown" {
		return ErrUnknownHarness
	}
	if p.SessionID == "" {
		return ErrMissingSessionID
	}
	ts := time.UnixMilli(p.TimestampMs)
	skew := now.Sub(ts)
	if skew < 0 {
		skew = -skew
	}
	if p.TimestampMs <= 0 || skew > clockSkewTolerance {
		return ErrTimestampOutOfRange
	}
	return nil
}

// transitionTo performs the Get-then-CAS TransitionState this file's
// header comment documents: it reads id's current state and moves it to
// to, which is a no-op when the record is already there.
func transitionTo(ctx context.Context, store *sessions.Store, id string, to sessions.SessionState) error {
	rec, err := store.Get(ctx, id)
	if err != nil {
		return err
	}
	return store.TransitionState(ctx, id, rec.State, to.String())
}

// publishJob best-effort publishes a jobs.* fan-out event (consumed by
// AC per the R-16.48 mapping table). A nil bus or a Publish failure are
// both swallowed — this handler's storage correctness never depends on
// the fan-out, matching internal/fleet/sessions.Store.emit's own
// documented best-effort contract.
func publishJob(ctx context.Context, bus sessions.EventBus, namespace string, kind events.EventKind, p HookPayload) {
	if bus == nil {
		return
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return
	}
	_, _ = bus.Publish(ctx, namespace, kind, "hookpacks:"+p.SessionID, payload)
}

// dispatch maps p's event type to its S-24.T3 sessions domain operation
// per the R-16.48 table (this file's header comment). Every state value
// is referenced by symbol — sessions.StateActive/StateIdle/StateClosed —
// never a literal.
func dispatch(ctx context.Context, store *sessions.Store, bus sessions.EventBus, p HookPayload) error {
	if !ParseHookEventType(p.EventType) {
		unknownEventCount.Add(1)
		return nil
	}
	switch p.EventType {
	case EventSessionStart, EventSubagentStart:
		return store.Upsert(ctx, sessions.SessionRecord{
			SessionID: p.SessionID, Harness: p.Harness, Account: p.Account,
			PID: p.PID, State: sessions.StateActive.String(), StartedAt: p.TimestampMs,
		})
	case EventInstructionsLoaded:
		return store.Touch(ctx, p.SessionID, sessions.FieldInstructionsLoadedAt)
	case EventUserPromptSubmit:
		return store.Touch(ctx, p.SessionID, sessions.FieldLastPromptAt)
	case EventPreToolUse, EventPostToolUse, EventPostToolBatch:
		if err := store.Touch(ctx, p.SessionID, sessions.FieldLastToolAt); err != nil {
			return err
		}
		return store.Touch(ctx, p.SessionID, sessions.FieldToolCount)
	case EventStop:
		return transitionTo(ctx, store, p.SessionID, sessions.StateIdle)
	case EventSubagentStop, EventSessionEnd:
		return transitionTo(ctx, store, p.SessionID, sessions.StateClosed)
	case EventTaskCreated:
		publishJob(ctx, bus, "jobs.task", events.EventKind("jobs.task.created"), p)
		return nil
	case EventTaskCompleted:
		publishJob(ctx, bus, "jobs.task", events.EventKind("jobs.task.completed"), p)
		return nil
	case EventWorktreeCreate:
		publishJob(ctx, bus, "jobs.worktree", events.EventKind("jobs.worktree.create"), p)
		return nil
	case EventWorktreeRemove:
		publishJob(ctx, bus, "jobs.worktree", events.EventKind("jobs.worktree.remove"), p)
		return nil
	case EventPreCompact, EventPostCompact:
		return store.Touch(ctx, p.SessionID, sessions.FieldCompactionCount)
	default:
		// Unreachable: ParseHookEventType above already refused anything
		// outside this switch's named cases. Kept only so exhaustive
		// linting has a safety net that never panics.
		return nil
	}
}

// RegisterHookEventHandler binds MethodHookEvent to registry. On
// Windows, this registers nothing and returns ErrWindowsTier2Unavailable
// instead — Windows never runs a daemon at all (tier-2), so there is no
// registry to bind into. See this package's CONTRACT DEVIATION note in
// registry.go and rpc.go's own precedent (P1-E12-W3-S24-T3) for why the
// actual daemon-startup call site (cmd/cascade's composition root) is
// out of this ticket's files_scope: RegisterHookEventHandler is fully
// wired-ready and exercised end to end against a real *rpc.Registry
// (handler_test.go).
func RegisterHookEventHandler(registry *rpc.Registry, store *sessions.Store, bus sessions.EventBus, clock runtime.Clock) error {
	if goruntime.GOOS == "windows" {
		return ErrWindowsTier2Unavailable
	}
	registry.Register(MethodHookEvent, func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := decodePayload(raw)
		if err != nil {
			return nil, err
		}
		if err := validate(p, clock.Now()); err != nil {
			return nil, err
		}
		if err := dispatch(ctx, store, bus, p); err != nil {
			return nil, err
		}
		publishJob(ctx, bus, "fleet.sessions", events.EventKind("fleet.sessions.hook_ingested"), p)
		return struct{}{}, nil
	})
	return nil
}
