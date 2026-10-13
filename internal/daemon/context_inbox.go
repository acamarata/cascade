package daemon

// Purpose: the context.slice half of addressed inbox hydration
//   (P1-E37-W8-S73-T1, R-14.323/Z2-41): the caller-side
//   InboxHydrationSource seam, the WithInboxHydration option
//   RegisterContextAssembleHandler takes, the wire view of one drained
//   envelope, and LiveSessionInboxSource, which drains ONLY for a session
//   the fleet sessions store knows and has not closed.
// Inputs: an InboxHydrationSource (cmd/cascade/daemon_unix_inbox_hydration.go
//   adapts the cascade-claude bridge to it; internal/daemon may not import
//   internal/plugins — the would-cycle rule) and a *sessions.Store.
// Outputs: Inbox on ContextSliceResult.
// Constraints: hydration is context, not policy. An empty, unknown or
//   closed session drains NOTHING (the same rule P1-E18-W7-S102-T1 applies
//   to directives) and a drain error returns no envelopes with the slice
//   unaffected. Draining never acknowledges the source inbox record, so an
//   envelope lost after a drain is still readable through `cascade inbox`.
// SPORT: internal/daemon context.slice inbox hydration (ADD,
//   P1-E37-W8-S73-T1).

import (
	"context"

	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
)

// InboxEnvelopeView is one drained envelope on the context.slice wire: the
// R-21.178 envelope, the R-16.5 metadata and the provenance tag. Every
// string arrives already escaped by its producer; the hook renders it as
// an untrusted-source data block.
type InboxEnvelopeView struct {
	ID            string `json:"id"`
	Class         string `json:"class"`
	OriginScope   string `json:"origin_scope"`
	DataClass     string `json:"data_class"`
	Summary       string `json:"summary"`
	TargetScope   string `json:"target_scope"`
	TargetSession string `json:"target_session"`
	TargetTask    string `json:"target_task"`
	Visibility    string `json:"visibility"`
	CorrelationID string `json:"correlation_id"`
	ExpiresAtMs   int64  `json:"expires_at_ms,omitempty"`
	Provenance    string `json:"provenance"`
}

// InboxHydrationSource hands back a session's pending envelopes, draining
// them. Declared here, in the caller's package, so internal/daemon never
// imports the adapter that implements it.
type InboxHydrationSource interface {
	Drain(ctx context.Context, sessionID string) ([]InboxEnvelopeView, error)
}

// WithInboxHydration makes context.slice drain src for the request's
// session id and return the envelopes as Inbox, under the same caller rule
// as the supervision directives (callerBoundTo). A nil src leaves it off.
func WithInboxHydration(src InboxHydrationSource) ContextOption {
	return func(o *contextOptions) { o.inbox = src }
}

// callerBoundTo reports the SUP-04 drain rule's caller half: a caller in
// ctx that is the owner CLI or a principal bound to sessionID.
func callerBoundTo(ctx context.Context, sessionID string) bool {
	caller, ok := principal.CallerFrom(ctx)
	if !ok || sessionID == "" {
		return false
	}
	return caller.OwnerCLI || (caller.Principal != nil && caller.Principal.SessionID == sessionID)
}

// drainInbox is the handler's drain step: nothing without a source, a
// session id or an admitted caller, and nothing on a drain error (fail-open
// for context: the slice itself is never affected).
func (o contextOptions) drainInbox(ctx context.Context, sessionID string) []InboxEnvelopeView {
	if o.inbox == nil || !callerBoundTo(ctx, sessionID) {
		return nil
	}
	views, err := o.inbox.Drain(ctx, sessionID)
	if err != nil || len(views) == 0 {
		return nil
	}
	return views
}

// LiveSessionInboxSource wraps inner so it is drained only for a session
// the fleet sessions store holds in a non-closed state. Any other session
// id — unknown, closed, empty — returns nothing WITHOUT touching inner, so
// its pending envelopes stay queued.
func LiveSessionInboxSource(live *sessions.Store, inner InboxHydrationSource) InboxHydrationSource {
	return liveSessionSource{live: live, inner: inner}
}

type liveSessionSource struct {
	live  *sessions.Store
	inner InboxHydrationSource
}

// Drain implements InboxHydrationSource. A store read failure other than
// not-found is returned (the handler then returns no envelopes).
func (s liveSessionSource) Drain(ctx context.Context, sessionID string) ([]InboxEnvelopeView, error) {
	if s.live == nil || s.inner == nil || sessionID == "" {
		return nil, nil
	}
	rec, err := s.live.Get(ctx, sessionID)
	if cascade.HasKind(err, cascade.KindNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if state, ok := sessions.ParseSessionState(rec.State); !ok || state == sessions.StateClosed {
		return nil, nil
	}
	return s.inner.Drain(ctx, sessionID)
}
