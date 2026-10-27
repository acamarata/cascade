package supervision

// Purpose (this file): a stall that is still there after a daemon restart is
// announced again, which is what makes the away-mode stall source safe to
// hand events over at-most-once. The bus commits a subscription cursor when it
// hands an event over, so a stall notice held in memory when the process dies
// is not replayed; this test proves the restarted detector re-seeds the session
// from the session store and publishes the same notification id (stall:<session>:<since>,
// where since is the stored progress mark) as a new episode, and that a
// reopened notify.BusStallSource receives it.

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/notify"
)

func TestStallPersistingAcrossRestartIsAnnouncedAgain(t *testing.T) {
	ctx := context.Background()
	fx := newStallFixture(t, fixtureOpts{})
	quiet := fx.nowMs() - (10 * time.Minute).Milliseconds()
	fx.sess.put("quiet", "active", quiet)
	want := stallNotificationID(StallEvent{SessionID: "quiet", StalledSince: quiet})

	stop, _ := startRun(t, fx.d, fx.bus)
	if err := fx.d.Poll(ctx); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got := fx.stalledEvents(); len(got) != 1 || got[0] != want {
		t.Fatalf("first announcement = %v, want [%s]", got, want)
	}
	if err := stop(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The daemon dies and restarts over the same stores. The first notice, if
	// it was only held in memory, is gone; the stall is still there.
	fx.d = rebuildDetector(t, fx)
	src, err := notify.NewBusStallSource(ctx, fx.bus, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewBusStallSource: %v", err)
	}
	srcCtx, cancel := context.WithCancel(ctx)
	runDone := make(chan error, 1)
	go func() { runDone <- src.Run(srcCtx) }()
	stop, _ = startRun(t, fx.d, fx.bus)
	if err := fx.d.Poll(ctx); err != nil {
		t.Fatalf("Poll after restart: %v", err)
	}
	var got []notify.StallNotice
	eventually(t, func() bool {
		got = append(got, src.DrainStallNotices()...)
		return len(got) > 0
	})
	if err := stop(); err != nil {
		t.Fatalf("Run after restart: %v", err)
	}
	cancel()
	if err := <-runDone; err != nil {
		t.Fatalf("source Run: %v", err)
	}
	got = append(got, src.DrainStallNotices()...)
	if len(got) != 1 || got[0].NotificationID != want || got[0].CorrelationID != "quiet" {
		t.Fatalf("restarted source drained %+v, want one notice for %s", got, want)
	}
}
