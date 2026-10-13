package audit

// Purpose: Verify's effect replay. The chain alone cannot see an index row
//   removed, rewritten or invented behind the API, so Verify compares the
//   eff: rows with what the chained effect records replay to.
// Constraints: stored state is asserted, never events; sentinels compared
//   by identity plus message; no sleeps (the concurrency test is bounded by
//   the writer finishing, not by time).
// SPORT: internal.audit.Log/ADDED (tests) (P1-SEC-06).

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const verifyKey = "plugin.update:verify-1"

// confirmedEffect begins and confirms one effect and returns its handle.
func confirmedEffect(t *testing.T, el *EffectLog, key string) EffectHandle {
	t.Helper()
	ctx := context.Background()
	h, err := el.BeginEffect(ctx, effectReq(key))
	if err != nil {
		t.Fatalf("BeginEffect: %v", err)
	}
	if _, err := el.ConfirmEffect(ctx, h, "done"); err != nil {
		t.Fatalf("ConfirmEffect: %v", err)
	}
	return h
}

func TestVerifyDetectsEffectIndexTamper(t *testing.T) {
	putRowFor := func(key string, row effectRow) func(provider.Store, EffectHandle) error {
		return func(s provider.Store, _ EffectHandle) error {
			data, err := encodeRow(row)
			if err != nil {
				return err
			}
			return s.Put(context.Background(), namespace, effectKey(key), data)
		}
	}
	tampers := map[string]struct {
		key    string
		tamper func(provider.Store, EffectHandle) error
	}{
		"row deleted": {verifyKey, func(s provider.Store, h EffectHandle) error {
			return s.Delete(context.Background(), namespace, effectKey(h.Key))
		}},
		"terminal rewritten to intent": {verifyKey, putRowFor(verifyKey, effectRow{Phase: EffectIntent, IntentSeq: 1})},
		"extra row for a never-begun key": {"never.begun:x",
			putRowFor("never.begun:x", effectRow{Phase: EffectIntent, IntentSeq: 1})},
		"row names another record": {verifyKey, putRowFor(verifyKey, effectRow{Phase: EffectConfirmed, IntentSeq: 2, TerminalSeq: 2})},
	}
	for _, s := range effectStores {
		t.Run(s.name, func(t *testing.T) {
			t.Run("control", func(t *testing.T) {
				store := s.open(t)
				el, log := newEffectLog(t, store)
				confirmedEffect(t, el, verifyKey)
				if err := log.Verify(context.Background()); err != nil {
					t.Fatalf("Verify on an untouched log = %v", err)
				}
			})
			for name, c := range tampers {
				t.Run(name, func(t *testing.T) {
					store := s.open(t)
					el, log := newEffectLog(t, store)
					h := confirmedEffect(t, el, verifyKey)
					if err := c.tamper(store, h); err != nil {
						t.Fatalf("tamper: %v", err)
					}
					err := log.Verify(context.Background())
					requireSentinel(t, err, ErrTampered)
					if !strings.Contains(err.Error(), c.key) {
						t.Fatalf("Verify error %q does not name the key %q", err, c.key)
					}
				})
			}
		})
	}
}

// Concurrent appends from another Log value move the log between Verify's
// walk and its index scan; that must never read as tampering.
func TestVerifyConcurrentAppendsNeverTampered(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteStore(t)
	_, verifier := newEffectLog(t, store)
	writerEL, _ := newEffectLog(t, store)

	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		for i := 0; i < 25; i++ {
			h, err := writerEL.BeginEffect(ctx, effectReq("plugin.update:conc-"+string(rune('a'+i))))
			if err == nil {
				_, err = writerEL.ConfirmEffect(ctx, h, "done")
			}
			if err != nil {
				t.Errorf("writer effect %d: %v", i, err)
				return
			}
		}
	}()
	for finished := false; !finished; {
		select {
		case <-done:
			finished = true
		default:
		}
		if err := verifier.Verify(ctx); err != nil && cascade.HasKind(err, cascade.KindIntegrity) {
			t.Errorf("Verify during concurrent appends = %v", err)
			break
		}
	}
	wg.Wait()
	if err := verifier.Verify(ctx); err != nil {
		t.Fatalf("Verify once the writers stopped = %v", err)
	}
}
