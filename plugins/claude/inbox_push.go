// Package claude (this file): InboxPush, the addressed-event delivery
// coordinator.
//
// Purpose: route one router-selected event to one active session: a direct
// CC channel push when the channel negotiated at Start and the event is
// addressed to that session, otherwise the durable next-turn
// hydration queue (the PRIMARY path, R-21.199). Never both.
//
// Inputs: a *CCChannel and an *InboxHydration, injected via NewInboxPush;
// Route takes the active session id and an InboxEvent the host adapter
// already filtered through the router's delivery and visibility rules.
// Outputs: AppendResult{SessionID, Appended} — Appended is true when a
// direct push put content into the session — and a typed error.
//
// Constraints: no decision here reads the event's content: only
// addressing and channel state decide the path, so a pushed payload can
// never trigger an action (R-21.154). No path acknowledges the source inbox
// record (S-73.T2 owns acknowledgement).
//
// SPORT: plugins/claude/inbox-push (ADD) — P1-E37-W8-S73-T1.
package claude

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/acamarata/cascade/pkg/cascade"
)

var (
	// ErrInboxPushNotStarted refuses Route before Start or after Stop.
	ErrInboxPushNotStarted = cascade.New(cascade.KindUnavailable, "claude: inbox push is not started")
	// ErrInboxPushWithheld refuses an addressed-class event whose target is
	// neither this session nor its task: nothing is pushed or hydrated.
	ErrInboxPushWithheld = cascade.New(cascade.KindInvalidInput, "claude: inbox event withheld: addressed to a different session or task")
)

// InboxPush is the delivery coordinator. Construct with NewInboxPush.
type InboxPush struct {
	channel   *CCChannel
	hydration *InboxHydration
	started   atomic.Bool

	mu       sync.Mutex
	deferral error
}

// NewInboxPush binds a channel and the hydration queue. Both are required.
func NewInboxPush(channel *CCChannel, hydration *InboxHydration) (*InboxPush, error) {
	if channel == nil || hydration == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "claude: NewInboxPush requires a channel and a hydration queue")
	}
	return &InboxPush{channel: channel, hydration: hydration}, nil
}

// Start negotiates the channel once and opens Route. A channel deferral is
// recorded (Deferral) and is not an error: hydration still delivers.
func (p *InboxPush) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return cascade.Wrap(cascade.KindCanceled, err, "claude: inbox push start canceled")
	}
	deferral := p.channel.Start(ctx)
	p.mu.Lock()
	p.deferral = deferral
	p.mu.Unlock()
	p.started.Store(true)
	return nil
}

// Deferral reports why the push path is off (nil when it negotiated).
func (p *InboxPush) Deferral() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.deferral
}

// Stop closes Route; a Route after Stop is refused.
func (p *InboxPush) Stop(context.Context) error {
	p.started.Store(false)
	return nil
}

// addressedTo reports whether ev names sessionID as its session. A task id
// is a different id namespace and never matches a session id, so an event
// that names only a task is withheld.
func addressedTo(sessionID string, ev InboxEvent) bool {
	return sessionID != "" && ev.TargetSession == sessionID
}

// Route delivers ev to sessionID: direct push when possible, otherwise the
// hydration queue. A direct push that fails for any reason falls back to
// hydration, so a channel rejection never loses the event. An
// addressed-class event for another session or task is withheld.
func (p *InboxPush) Route(ctx context.Context, sessionID string, ev InboxEvent) (AppendResult, error) {
	result := AppendResult{SessionID: sessionID}
	if !p.started.Load() {
		return result, ErrInboxPushNotStarted
	}
	if err := ctx.Err(); err != nil {
		return result, cascade.Wrap(cascade.KindCanceled, err, "claude: inbox route canceled")
	}
	addressed := addressedTo(sessionID, ev)
	if ev.Class == "addressed" && !addressed {
		return result, ErrInboxPushWithheld
	}
	if addressed && p.pushDirect(ctx, sessionID, ev) {
		result.Appended = true
		return result, nil
	}
	return result, p.hydration.Enqueue(ctx, sessionID, ev)
}

// pushDirect attempts the channel and reports success. Any error — a
// deferral, a rejection, a cancellation, an envelope refusal — is a "no",
// and Route falls back to hydration.
func (p *InboxPush) pushDirect(ctx context.Context, sessionID string, ev InboxEvent) bool {
	env, err := BuildEnvelope(ev, ev.DataClass)
	if err != nil {
		return false
	}
	return p.channel.Deliver(ctx, sessionID, env) == nil
}
