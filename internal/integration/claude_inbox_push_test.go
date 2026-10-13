//go:build !windows && integration

// Package integration holds Article-2 real-counterpart tests that span more
// than one internal package.
//
// Purpose (this file): replay the provenance-stamped CC channel fixtures
// (plugins/claude/testdata/cc-channel) and a W/S-49.T1 router delivery
// through the real router (internal/notify.Service), the real host adapter
// (internal/plugins.ClaudeInboxBridge + WireClaudeInboxPush), the real
// channel probe (production default: indeterminate, so the push path is
// deferred) and the P/S-34.T4 hydration drain, over a real SQLite store
// under t.TempDir() (P1-E37-W8-S73-T1).
// Inputs: the two fixture files; no network.
// Outputs: pass/fail.
// Constraints: the fixture's captured handshake must still carry the MCP
// protocolVersion the channel pins; the hydrated record must carry the
// summary-only envelope, never the body.
// SPORT: internal/integration cascade-claude-inbox-push (ADD) —
// P1-E37-W8-S73-T1.
package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/plugins/claude"
	"github.com/acamarata/cascade/providers/sqlite"
)

type inboxFixedClock struct{ now time.Time }

func (c inboxFixedClock) Now() time.Time { return c.now }

// readCapturedFixtures asserts the two fixtures still describe the shape
// the channel speaks: MCP protocolVersion 2025-11-25 and the
// notifications/claude/channel {content, meta} notification.
func readCapturedFixtures(t *testing.T) {
	t.Helper()
	dir := filepath.Join("..", "..", "plugins", "claude", "testdata", "cc-channel")
	var neg struct {
		Request struct {
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		} `json:"request"`
	}
	var delivery struct {
		A struct {
			Notification struct {
				Method string `json:"method"`
				Params struct {
					Content string            `json:"content"`
					Meta    map[string]string `json:"meta"`
				} `json:"params"`
			} `json:"notification"`
		} `json:"A_sent_by_this_session"`
	}
	for name, into := range map[string]any{"protocol-negotiation.json": &neg, "addressed-inbox-delivery.json": &delivery} {
		raw, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // fixed repo-relative fixture path
		if err != nil || json.Unmarshal(raw, into) != nil {
			t.Fatalf("fixture %s unreadable: %v", name, err)
		}
	}
	if neg.Request.Params.ProtocolVersion != "2025-11-25" || delivery.A.Notification.Method != "notifications/claude/channel" ||
		delivery.A.Notification.Params.Meta["event_id"] == "" {
		t.Fatalf("captured fixtures no longer match the pinned channel shape: %+v / %+v", neg, delivery.A.Notification)
	}
}

func TestClaudeInboxPushRecordedCounterpart(t *testing.T) {
	readCapturedFixtures(t)
	ctx := context.Background()
	clock := inboxFixedClock{now: time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)}
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "cascade.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	attention := supervision.NewStore(store, clock, nil, supervision.NewSystemIDGenerator(), 0)
	bridge, err := plugins.NewClaudeInboxBridge(ctx, store, clock, attention)
	if err != nil {
		t.Fatal(err)
	}
	svc := notify.NewService(notify.DefaultConfig(), clock, nil)
	unwire, err := plugins.WireClaudeInboxPush(ctx, bridge, svc.Registry, notify.CandidateSession{SessionID: "cc-session-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer unwire()
	body := "COUNTERPART-BODY " + strings.Repeat("never on the wire ", 20)
	if err := svc.Notifier.Deliver(ctx, notify.Notification{
		ID: "counterpart-1", Class: notify.ClassAddressed, TargetSession: "cc-session-1",
		OriginScope: "repo-a", Visibility: notify.VisibilityPrivate, Payload: []byte(body),
	}); err != nil {
		t.Fatal(err)
	}
	svc.Dispatcher.Drain(ctx)
	recs, res, err := bridge.ReadNextTurnForSession(ctx, "cc-session-1")
	if err != nil || !res.Appended || len(recs) != 1 {
		t.Fatalf("hydration = (%+v, %+v, %v), want the one addressed record", recs, res, err)
	}
	block := claude.RenderInboxBlock(recs)
	if strings.Contains(block, body) || strings.Contains(block, strings.Repeat("never on the wire ", 10)) ||
		!strings.Contains(block, `provenance="untrusted-source"`) || recs[0].DataClass != "restricted" {
		t.Fatalf("hydrated block carries the body or lacks its tags:\n%s", block)
	}
}
