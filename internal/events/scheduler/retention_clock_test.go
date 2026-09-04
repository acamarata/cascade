// Purpose: the W1-hardening-gate regression test for the retention
//
//	prune runnable's clock. The composition root registers these jobs
//	with a ZERO storage.RetentionConfig, and DomainPruner.Prune refuses
//	with KindInvalidInput when cfg.Clock is nil, so before this test the
//	shipped daemon's prune job returned that error on every fire, for the
//	life of the process. Every existing test passed a config with a Clock
//	already set, which is exactly why nothing caught it.
//
// SPORT: internal.events.scheduler.RegisterRetentionJobs/CHANGED (P1-E04-W1-S07-T7).

package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

// TestRegisterRetentionJobs_ZeroConfigPruneRunnableSucceeds fires the
// registered prune runnable against a real database with the config
// shape production actually passes: the zero value. It must succeed.
//
// With no DomainRetention windows and no Targets configured, a successful
// Prune deletes nothing, which is the honest "no retention policy is
// configured yet" state. That is a different thing from an error, and the
// difference is the whole finding: an erroring job looks like a broken
// daemon and prunes nothing either way.
func TestRegisterRetentionJobs_ZeroConfigPruneRunnableSucceeds(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(testEpoch)
	bus := events.New(store, clock)
	sched := New(store, testNamespace, clock, bus, "owner-a", time.Hour)

	// db is nil for the same reason the sibling registration test uses
	// nil: with no DomainRetention window configured, pruneDomain issues
	// no statement for any domain, so the handle is never dereferenced.
	// The nil-Clock refusal happens before that point regardless, which
	// is precisely what this test distinguishes.
	//
	// storage.RetentionConfig{} is byte for byte what
	// cmd/cascade/daemon_unix_scheduler.go passes.
	if rerr := RegisterRetentionJobs(ctx, sched, nil, storage.RetentionConfig{}, clock); rerr != nil {
		t.Fatalf("RegisterRetentionJobs: %v", rerr)
	}

	run, ok := sched.runnables[retentionPruneOwner]
	if !ok {
		t.Fatalf("no runnable registered for %q", retentionPruneOwner)
	}
	if rerr := run(ctx); rerr != nil {
		if strings.Contains(rerr.Error(), "non-nil Clock") {
			t.Fatalf("the prune runnable registered from a zero config refuses on every fire: %v", rerr)
		}
		t.Fatalf("prune runnable returned an unexpected error: %v", rerr)
	}
}
