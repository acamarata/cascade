package supervision

// Purpose (this file): the first two escalation rungs for a stalled
// session, as real governor.Retryer and governor.ContextEnricher
// satisfiers. Both are state-driven: the answer depends on the session
// store's record for the session and on whether a delivery channel exists,
// never on a constant. Both leave a fixed daemon-authored directive in the
// DirectiveStore for the session's consumer to drain.
//
// Inputs: RungConfig (a SessionLookup, the DirectiveStore, whether
// [context.hydration] is enabled, the context budget) and the detector's
// own stall record lookup (so the retry text can name when the stall began).
// Outputs: an enqueued Directive, or a typed refusal.
// Constraints: directive text only ever interpolates an RFC3339 time and a
// StallKind enum value; never session, event or user content. Errors come
// from the frozen kind set.
//
// SPORT: fleet.supervision.stall-rungs/ADDED (P1-SUP-03).

import (
	"context"
	"fmt"
	"time"

	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/pkg/cascade"
)

// SessionLookup is the read side of the sessions domain the rungs and the
// detector need. *sessions.Store satisfies it.
type SessionLookup interface {
	Get(ctx context.Context, id string) (sessions.SessionRecord, error)
	List(ctx context.Context, f sessions.Filter) ([]sessions.SessionRecord, error)
}

// RungConfig carries what the retry and context rungs need.
type RungConfig struct {
	Sessions         SessionLookup
	Directives       *DirectiveStore
	HydrationEnabled bool
	BudgetTokens     int
}

const (
	retryDirectiveFormat  = "Cascade supervision: this session has made no progress since %s (%s). Re-read the task, re-run the last failing step, and report its result."
	contextDirectiveText  = "Cascade supervision: extra context budget granted for this turn."
	noDeliveryChannelText = "no delivery channel: [context.hydration].enabled is false"
)

// sessionRetryer implements governor.Retryer (rung 1).
type sessionRetryer struct {
	cfg    RungConfig
	lookup stallLookup
}

// sessionEnricher implements governor.ContextEnricher (rung 2).
type sessionEnricher struct {
	cfg    RungConfig
	lookup stallLookup
}

var (
	_ governor.Retryer         = (*sessionRetryer)(nil)
	_ governor.ContextEnricher = (*sessionEnricher)(nil)
)

// precheck refuses unless id names a live session and a delivery channel
// exists. Every refusal is typed.
func (c RungConfig) precheck(ctx context.Context, id string) error {
	if c.Sessions == nil || c.Directives == nil {
		return cascade.New(cascade.KindUnavailable, "supervision: rung has no session lookup or directive store")
	}
	rec, err := c.Sessions.Get(ctx, id)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return cascade.Wrap(cascade.KindNotFound, err, "supervision: session not found")
		}
		return cascade.Wrap(cascade.KindUnavailable, err, "supervision: session lookup failed")
	}
	state, ok := sessions.ParseSessionState(rec.State)
	if !ok {
		return cascade.New(cascade.KindInvalidInput, "supervision: unrecognised session state")
	}
	if state == sessions.StateClosed {
		return cascade.New(cascade.KindNotFound, "supervision: session is closed")
	}
	if !c.HydrationEnabled {
		return cascade.New(cascade.KindUnavailable, noDeliveryChannelText)
	}
	return nil
}

// stalledSince returns the stall record's start (0 when unknown) and kind.
func stalledSince(lookup stallLookup, id string) (int64, StallKind) {
	if lookup == nil {
		return 0, StallKindUnknown
	}
	ev, ok := lookup(id)
	if !ok {
		return 0, StallKindUnknown
	}
	return ev.StalledSince, ev.StallKind
}

// retryText builds the fixed retry directive: only an RFC3339 time (or
// "unknown") and a StallKind value are interpolated.
func retryText(since int64, kind StallKind) string {
	when := "unknown"
	if since > 0 {
		when = time.UnixMilli(since).UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf(retryDirectiveFormat, when, string(kind))
}

// Retry implements governor.Retryer.
func (r *sessionRetryer) Retry(ctx context.Context, entityID string) error {
	if err := r.cfg.precheck(ctx, entityID); err != nil {
		return err
	}
	since, kind := stalledSince(r.lookup, entityID)
	return r.cfg.Directives.Enqueue(ctx, Directive{SessionID: entityID, Kind: DirectiveRetry, Text: retryText(since, kind), StalledSince: since})
}

// Enrich implements governor.ContextEnricher; added is the configured
// context budget.
func (e *sessionEnricher) Enrich(ctx context.Context, entityID string) (int, error) {
	if err := e.cfg.precheck(ctx, entityID); err != nil {
		return 0, err
	}
	since, _ := stalledSince(e.lookup, entityID)
	if err := e.cfg.Directives.Enqueue(ctx, Directive{SessionID: entityID, Kind: DirectiveContext, Text: contextDirectiveText, StalledSince: since}); err != nil {
		return 0, err
	}
	return e.cfg.BudgetTokens, nil
}
