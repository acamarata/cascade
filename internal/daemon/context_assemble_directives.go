package daemon

// Purpose: the supervision-directive drain on context.slice. A stalled
//   session's retry and context directives wait in the supervision
//   directive queue until the next slice for that session; this file
//   decides whether a request may drain them and applies what it drained.
//
// Inputs: the request's ContextAssembleParams.SessionID, the transport-
//   verified caller in ctx (principal.CallerFrom) and an optional
//   DirectiveDrainer the composition root supplies.
//
// Outputs: ContextSliceResult.Directives, and a doubled token budget once
//   when a context directive was drained.
//
// Constraints: the drain is fail-closed. It happens only when a session id
//   is named, a drainer is set, a caller is in ctx, and that caller is the
//   owner CLI or a principal bound to that same session. Every other
//   request, including one with no caller at all, gets the unchanged slice
//   and leaves the queue untouched. The live drainer (NewLiveDirectiveDrainer)
//   drains only a known session that is not closed, checked before the
//   queue is read. A drain error returns the slice without directives.
//   A slice that fails is returned before anything is drained, so a
//   directive is never consumed by a request that got no slice. Directive
//   text is never logged.
//
// SPORT: internal/daemon (CHANGED, context.slice supervision directives).

import (
	"context"
	"math"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/principal"
)

// DirectiveDrainer drains a live session's pending supervision directives.
// retry holds the text of every drained directive in enqueue order (retry
// and context directives alike); contextBoost reports that at least one was
// a context directive. An unknown or closed session drains nothing and
// leaves its queue untouched.
type DirectiveDrainer interface {
	DrainLive(ctx context.Context, sessionID string) (retry []string, contextBoost bool, err error)
}

// ContextOption configures RegisterContextAssembleHandler.
type ContextOption func(*contextOptions)

// contextOptions is what the ContextOptions set. compute is the slice
// assembler, ComputeContextSlice unless a package test replaces it.
type contextOptions struct {
	drainer DirectiveDrainer
	inbox   InboxHydrationSource // WithInboxHydration (context_inbox.go)
	compute func(context.Context, scope.ResolveDeps, ContextAssembleParams) (ContextSliceResult, error)
}

// WithDirectiveDrain makes context.slice drain the request's session
// directives through d, subject to the caller rule in mayDrain. A nil d
// leaves the drain off.
func WithDirectiveDrain(d DirectiveDrainer) ContextOption {
	return func(o *contextOptions) { o.drainer = d }
}

// applyContextOptions folds opts into one contextOptions value.
func applyContextOptions(opts []ContextOption) contextOptions {
	o := contextOptions{compute: ComputeContextSlice}
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	return o
}

// mayDrain is the drain rule: a named session, a drainer, and a caller in
// ctx that is the owner CLI or a principal bound to that same session.
func (o contextOptions) mayDrain(ctx context.Context, sessionID string) bool {
	if sessionID == "" || o.drainer == nil {
		return false
	}
	caller, ok := principal.CallerFrom(ctx)
	if !ok {
		return false
	}
	if caller.OwnerCLI {
		return true
	}
	return caller.Principal != nil && caller.Principal.SessionID == sessionID
}

// sliceWithDirectives is context.slice's handler body: the slice first,
// then the drain, then (on a context directive) one re-assembly at twice
// the budget. A failed re-assembly keeps the first slice, so drained
// directives are still delivered.
func (o contextOptions) sliceWithDirectives(ctx context.Context, deps scope.ResolveDeps, p ContextAssembleParams) (ContextSliceResult, error) {
	result, err := o.compute(ctx, deps, p)
	if err != nil || !o.mayDrain(ctx, p.SessionID) {
		return result, err
	}
	texts, boost, err := o.drainer.DrainLive(ctx, p.SessionID)
	if err != nil || len(texts) == 0 {
		return result, nil
	}
	if boost {
		boosted := p
		doubled := doubledBudget(p.MaxTokens)
		boosted.MaxTokens = &doubled
		if again, err := o.compute(ctx, deps, boosted); err == nil {
			result = again
		}
	}
	result.Directives = texts
	return result, nil
}

// doubledBudget is twice the request's budget (the default when unset),
// held at math.MaxInt rather than overflowing.
func doubledBudget(maxTokens *int) int {
	budget := defaultContextMaxTokens
	if maxTokens != nil {
		budget = *maxTokens
	}
	if budget > math.MaxInt/2 {
		return math.MaxInt
	}
	return budget * 2
}

// SessionGetter is the one sessions read the live drainer needs.
// *sessions.Store satisfies it.
type SessionGetter interface {
	Get(ctx context.Context, id string) (sessions.SessionRecord, error)
}

// liveDirectiveDrainer is the production DirectiveDrainer: the sessions
// store decides liveness, the supervision directive store holds the queue.
type liveDirectiveDrainer struct {
	sessions   SessionGetter
	directives *supervision.DirectiveStore
}

// NewLiveDirectiveDrainer returns the DirectiveDrainer over sessions and
// directives. A nil collaborator drains nothing.
func NewLiveDirectiveDrainer(sessions SessionGetter, directives *supervision.DirectiveStore) DirectiveDrainer {
	return liveDirectiveDrainer{sessions: sessions, directives: directives}
}

// DrainLive drains sessionID's directives when the session is known and
// not closed. The session check runs before the drain, so a refused
// session never touches the queue.
func (d liveDirectiveDrainer) DrainLive(ctx context.Context, sessionID string) ([]string, bool, error) {
	if d.sessions == nil || d.directives == nil || sessionID == "" {
		return nil, false, nil
	}
	rec, err := d.sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, false, err
	}
	state, ok := sessions.ParseSessionState(rec.State)
	if !ok || state == sessions.StateClosed {
		return nil, false, nil
	}
	drained, err := d.directives.Drain(ctx, sessionID)
	if err != nil {
		return nil, false, err
	}
	texts := make([]string, 0, len(drained))
	boost := false
	for _, dir := range drained {
		texts = append(texts, dir.Text)
		boost = boost || dir.Kind == supervision.DirectiveContext
	}
	return texts, boost, nil
}
