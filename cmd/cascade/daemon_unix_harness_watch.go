//go:build !windows

package main

import (
	"context"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/plugins"
)

// Purpose: the daemon composition root's call site for the cascade-claude
//
//	session watch — the one place the watch becomes reachable from a
//	shipping binary.
//
// Inputs: the daemon's manifest, its event bus, and its own socket path.
// Outputs: the watch registered as a tracked daemon subsystem.
// Constraints: unix-only by build tag, matching the rest of the daemon
//
//	composition root: Windows tier-2 has no daemon, so there is no stream
//	to subscribe to and nothing to register.
//
//	The watch subscribes to the daemon's OWN socket. That is deliberate and
//	not a loop: the session stream is produced by the fleet session store
//	inside this daemon and consumed here through the same public /events
//	endpoint any other client uses, so the watch sees exactly what an
//	external subscriber would rather than a privileged internal view.
//
// SPORT: cmd/cascade:harness-watch (ADD) — P1-E16-W4-S34-T1.

// wireHarnessSessionWatch registers cascade-claude's session watch.
//
// It never fails the daemon's startup: a harness watch that cannot run is a
// missing observation, not a broken daemon, and the manifest records its
// state either way for `cascade daemon status` to report.
func wireHarnessSessionWatch(ctx context.Context, manifest *daemon.Manifest, bus *events.Bus, socketPath string) {
	if bus == nil || socketPath == "" {
		manifest.RegisterHarnessSessionWatch(ctx, nil)
		return
	}
	watcher := plugins.NewClaudeSessionWatcher(claudeSessionStream(client.UnixDialer, socketPath), bus)
	manifest.RegisterHarnessSessionWatch(ctx, watcher.Run)
}

// claudeSessionStream opens the daemon's session stream for the watch.
//
// This is the dial, and it lives here rather than in internal/plugins or
// internal/fleet/sessions on purpose: the composition root owns the sockets
// the process opens, and it already had to dial /events for
// `cascade fleet sessions --watch`. Both callers now share
// dialDaemonEvents; the fold that turns the stream back into records stays
// in the package that defines the wire format.
func claudeSessionStream(dial eventsDialer, socketPath string) plugins.SessionStreamOpener {
	return func(ctx context.Context) (<-chan sessions.SessionRecord, func(), error) {
		body, release, err := dialDaemonEvents(ctx, dial, socketPath, sessions.Topic)
		if err != nil {
			return nil, nil, err
		}
		records := make(chan sessions.SessionRecord, 16)
		go func() {
			defer close(records)
			defer release()
			sessions.ReadRecords(ctx, body, records)
		}()
		return records, release, nil
	}
}
