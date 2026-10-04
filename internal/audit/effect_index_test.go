package audit

// Purpose: the effect index tamper tests: a row deleted, rewritten or
//   malformed behind the API, and what recovery reports for each. Every
//   assertion reads stored state back, never events.
// Constraints: Art.7.1 (files under t.TempDir), frozen clock, no sleeps;
//   sentinels are compared by identity plus message, because errors.Is on
//   a cascade error compares Kind only.
// SPORT: internal.audit.EffectLog/ADDED (tests) (P1-SEC-33).

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

// putRow overwrites a key's index row behind the API.
func putRow(row string) func(provider.Store, EffectHandle) error {
	return func(s provider.Store, h EffectHandle) error {
		return s.Put(context.Background(), namespace, effectKey(h.Key), []byte(row))
	}
}

func TestEffectIndexTamperRefused(t *testing.T) {
	tampers := map[string]func(provider.Store, EffectHandle) error{
		"row deleted": func(s provider.Store, h EffectHandle) error {
			return s.Delete(context.Background(), namespace, effectKey(h.Key))
		},
		"row names another record": func(s provider.Store, h EffectHandle) error {
			return s.Put(context.Background(), namespace, effectKey(h.Key), []byte(`{"phase":"intent","intent_seq":1,"terminal_seq":0}`))
		},
		"row not JSON":        putRow("{"),
		"row phase unknown":   putRow(`{"phase":"done","intent_seq":2,"terminal_seq":2}`),
		"row names no record": putRow(`{"phase":"intent","intent_seq":99,"terminal_seq":0}`),
		"row claims terminal": func(s provider.Store, h EffectHandle) error {
			return s.Put(context.Background(), namespace, effectKey(h.Key),
				[]byte(`{"phase":"confirmed","intent_seq":2,"terminal_seq":2}`))
		},
	}
	for name, tamper := range tampers {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := storetest.NewMemStore()
			el, log := newEffectLog(t, store)
			if _, err := log.Append(ctx, sampleEvent(1)); err != nil {
				t.Fatalf("Append: %v", err)
			}
			h, err := el.BeginEffect(ctx, effectReq("plugin.update:p-9"))
			if err != nil {
				t.Fatalf("BeginEffect: %v", err)
			}
			if err := tamper(store, h); err != nil {
				t.Fatalf("tamper: %v", err)
			}
			pending, err := el.PendingEffects(ctx)
			requireSentinel(t, err, ErrTampered)
			if len(pending) != 0 {
				t.Fatalf("PendingEffects returned %+v alongside an integrity failure", pending)
			}
			_, found, serr := el.EffectState(ctx, h.Key)
			if name == "row deleted" {
				// Known gap: a deleted row reads as "never begun". Recovery
				// is what detects it; see TestEffectDeletedRowDetectedAtRecovery.
				if found || serr != nil {
					t.Fatalf("EffectState after the row was deleted = found %v, err %v; want false, nil", found, serr)
				}
				return
			}
			if found && serr == nil {
				t.Fatal("EffectState accepted a tampered index row")
			}
		})
	}
}

// A deleted index row lets the key begin again and Verify stays clean, but
// PendingEffects names the second intent, so recovery refuses.
func TestEffectDeletedRowDetectedAtRecovery(t *testing.T) {
	for _, s := range effectStores {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			store := s.open(t)
			el, log := newEffectLog(t, store)
			h, err := el.BeginEffect(ctx, effectReq("plugin.update:p-7"))
			if err != nil {
				t.Fatalf("BeginEffect: %v", err)
			}
			if err := store.Delete(ctx, namespace, effectKey(h.Key)); err != nil {
				t.Fatalf("delete row: %v", err)
			}
			h2, err := el.BeginEffect(ctx, effectReq("plugin.update:p-7"))
			if err != nil {
				t.Fatalf("BeginEffect after the row was deleted: %v", err)
			}
			if h2.IntentSeq == h.IntentSeq {
				t.Fatalf("second intent reused seq %d", h.IntentSeq)
			}
			if err := log.Verify(ctx); err != nil {
				t.Fatalf("Verify = %v; the log chain itself is intact", err)
			}
			pending, err := el.PendingEffects(ctx)
			requireSentinel(t, err, ErrTampered)
			if want := fmt.Sprintf("record %d (", h2.IntentSeq); !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not name the second intent (%q)", err, want)
			}
			if len(pending) != 0 {
				t.Fatalf("PendingEffects returned %+v alongside an integrity failure", pending)
			}
		})
	}
}
