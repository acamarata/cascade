package plugins

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/claude"
)

// Purpose (this file): the other half of cascade-claude's composition root
//
//	— the session watch's two seams, wired to the real fleet session stream
//	and the real daemon event bus.
//
// Inputs: an opener for the daemon's session stream; the event bus.
// Outputs: a claude.Watcher whose Subscribe and Emit reach real
//
//	counterparts, so the watch is reachable from a running program rather
//	than only from its own tests.
//
// Constraints: this file exists because plugins/** may not import
//
//	internal/** (Art.10.2). The plugin owns the lifecycle DECISIONS (what a
//	transition means, when to synthesize a stop, how to back off); this file
//	owns only the translation between its types and the fleet's.
//
//	It does NOT dial. The stream arrives as an opener the daemon's
//	composition root supplies, because opening a socket belongs there
//	(journals/RULING-coverage-socket-code.md) — and because a dial inside
//	this package would put the translation loop below out of reach of the
//	default unit lane, which is where it is worth asserting.
//
// SPORT: internal/plugins cascade-claude watch wiring (ADD) — P1-E16-W4-S34-T1.

// claudeWatchNamespace is the event-bus namespace the watch publishes
// harness lifecycle events to.
const claudeWatchNamespace = "plugins.claude.sessions"

// NewClaudeSessionWatcher builds the watcher the daemon runs.
//
// It is a constructor rather than an init()-time global because both of its
// collaborators are per-daemon values that do not exist at package-init
// time: the stream is opened against a socket path resolved at startup, and
// the bus is constructed with the daemon's own store and clock.
func NewClaudeSessionWatcher(open SessionStreamOpener, bus *events.Bus) *claude.Watcher {
	return &claude.Watcher{
		Subscribe: claudeSessionSubscriber(open),
		Emit:      claudeLifecycleEmitter(bus),
	}
}

// SessionStreamOpener opens a stream of fleet session records.
//
// The daemon's composition root supplies the real one, which dials the
// daemon socket; a test supplies a channel directly. This is the seam that
// keeps the dial out of this package.
type SessionStreamOpener func(ctx context.Context) (<-chan sessions.SessionRecord, func(), error)

// claudeSessionSubscriber adapts the fleet session stream into the
// plugin's Subscriber seam.
func claudeSessionSubscriber(open SessionStreamOpener) claude.Subscriber {
	return func(ctx context.Context) (<-chan claude.SessionEvent, func(), error) {
		if open == nil {
			return nil, nil, cascade.New(cascade.KindInternal,
				"plugins: no session stream was wired for the cascade-claude watch")
		}
		records, release, err := open(ctx)
		if err != nil {
			return nil, nil, err
		}
		out := make(chan claude.SessionEvent, 16)
		go func() {
			defer close(out)
			for rec := range records {
				ev, ok := claudeSessionEvent(rec)
				if !ok {
					continue
				}
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
		}()
		return out, release, nil
	}
}

// claudeSessionEvent translates one fleet record into the plugin's event.
//
// The state mapping is the substantive part, and it is deliberately
// lossy in one direction only:
//
//   - "active" is running;
//   - "idle", "blocked" and "stalled" all mean ALIVE BUT NOT WORKING, which
//     is one lifecycle fact even though the fleet distinguishes three
//     degrees of it for its attention queue;
//   - "closed" is stopped, and it is terminal;
//   - "unknown" is DROPPED rather than mapped. It is the fleet's
//     fail-closed sentinel meaning "no basis for classification", so
//     turning it into any lifecycle event would invent a fact the fleet
//     explicitly declined to assert.
func claudeSessionEvent(rec sessions.SessionRecord) (claude.SessionEvent, bool) {
	var state claude.SessionState
	switch rec.State {
	case sessions.StateActive.String():
		state = claude.SessionRunning
	case sessions.StateIdle.String(), sessions.StateBlocked.String(), sessions.StateStalled.String():
		state = claude.SessionIdle
	case sessions.StateClosed.String():
		state = claude.SessionStopped
	default:
		return claude.SessionEvent{}, false
	}
	return claude.SessionEvent{
		ID:      rec.SessionID,
		State:   state,
		Harness: rec.Harness,
		PID:     rec.PID,
	}, true
}

// claudeLifecycleEmitter adapts the daemon event bus into the plugin's
// Emitter seam.
func claudeLifecycleEmitter(bus *events.Bus) claude.Emitter {
	return func(ctx context.Context, ev claude.LifecycleEvent) error {
		payload, err := json.Marshal(map[string]any{
			"session_id":  ev.Session.ID,
			"harness":     ev.Session.Harness,
			"pid":         ev.Session.PID,
			"state":       string(ev.Session.State),
			"transition":  string(ev.Transition),
			"synthesized": ev.Synthesized,
		})
		if err != nil {
			return cascade.Wrap(cascade.KindInternal, err,
				"internal/plugins: encoding a cascade-claude lifecycle event")
		}
		_, err = bus.Publish(ctx, claudeWatchNamespace,
			events.EventKind(string(ev.Transition)), claudePackName, payload)
		return err
	}
}
