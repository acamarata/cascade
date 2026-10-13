//go:build !windows

package main

// Purpose: the drain half of addressed inbox hydration: the adapter from the
//   cascade-claude bridge to the daemon's InboxHydrationSource seam, gated by
//   LiveSessionInboxSource on the fleet sessions store, so a hook payload's
//   session_id drains that session's addressed envelopes into the slice.
// Inputs: the daemon wiring (Store, Clock, Bus).
// Outputs: a daemon.InboxHydrationSource.
// Constraints: the drain adapter converts records to views field for field and
//   adds nothing; every value is already escaped by plugins/claude. This file
//   does not import internal/rpc (cmd-rpc-server boundary).
// SPORT: cmd/cascade daemon inbox hydration composition (P1-HRN-02).

import (
	"context"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/plugins/claude"
)

// newInboxBridge builds the cascade-claude bridge over the daemon store and
// its attention queue (global-critical items).
func newInboxBridge(ctx context.Context, w *daemonWiring) (*plugins.ClaudeInboxBridge, error) {
	return plugins.NewClaudeInboxBridge(ctx, w.Store, w.Clock, daemon.NewAttentionStore(w.Store, w.Clock, w.Bus))
}

// newBridgeInboxSource is the bridge drain behind the live-session gate.
func newBridgeInboxSource(ctx context.Context, w *daemonWiring) (daemon.InboxHydrationSource, error) {
	bridge, err := newInboxBridge(ctx, w)
	if err != nil {
		return nil, err
	}
	live := sessions.New(w.Store, w.Clock, w.Bus)
	return daemon.LiveSessionInboxSource(live, bridgeInboxSource{bridge: bridge}), nil
}

// bridgeInboxSource adapts the bridge's next-turn drain to the daemon's
// caller-side InboxHydrationSource seam.
type bridgeInboxSource struct {
	bridge *plugins.ClaudeInboxBridge
}

// Drain implements daemon.InboxHydrationSource.
func (s bridgeInboxSource) Drain(ctx context.Context, sessionID string) ([]daemon.InboxEnvelopeView, error) {
	recs, _, err := s.bridge.ReadNextTurnForSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	views := make([]daemon.InboxEnvelopeView, 0, len(recs))
	for _, r := range recs {
		views = append(views, inboxViewFromRecord(r))
	}
	return views, nil
}

// inboxViewFromRecord is the record-to-wire conversion.
func inboxViewFromRecord(r claude.InboxRecord) daemon.InboxEnvelopeView {
	return daemon.InboxEnvelopeView{
		ID: r.ID, Class: r.Class, OriginScope: r.OriginScope, DataClass: r.DataClass, Summary: r.Summary,
		TargetScope: r.TargetScope, TargetSession: r.TargetSession, TargetTask: r.TargetTask,
		Visibility: r.Visibility, CorrelationID: r.CorrelationID, ExpiresAtMs: r.ExpiresAtMs, Provenance: r.Provenance,
	}
}
