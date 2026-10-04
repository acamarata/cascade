package audit

// Purpose: the effect recorder's write-path tests: a key begun twice, a
//   terminal written twice, effect fields refused on the plain Append
//   path, a transaction failing part-way, and sixteen writers racing for
//   one key. Every assertion reads stored state back, never events.
// Constraints: Art.7.1 (files under t.TempDir), frozen clock, no sleeps;
//   sentinels are compared by identity plus message, because errors.Is on
//   a cascade error compares Kind only.
// SPORT: internal.audit.EffectLog/ADDED (tests) (P1-SEC-33).

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// effectStore names one backing store an effect test runs against.
type effectStore struct {
	name string
	open func(t *testing.T) provider.Store
}

// effectStores lists the stores every effect test covers. A postgres-tagged
// file appends the P1-SEC-37 store.
var effectStores = []effectStore{
	{"memstore", func(*testing.T) provider.Store { return storetest.NewMemStore() }},
	{"sqlite", newSQLiteStore},
}

func newEffectLog(t *testing.T, store provider.Store) (*EffectLog, *Log) {
	t.Helper()
	log := New(store, testkit.NewFrozenClock(testInstant), nil)
	el, err := NewEffectLog(log)
	if err != nil {
		t.Fatalf("NewEffectLog: %v", err)
	}
	return el, log
}

func effectReq(key string) EffectRequest {
	return EffectRequest{Kind: KindExternalEffect, Actor: "agent:test", Action: "clipboard.clear",
		Key: key, ParamsHash: HashParams([]byte(key))}
}

// requireSentinel walks err's chain for want itself (pointer identity) and
// checks the message carries want's text.
func requireSentinel(t *testing.T, err error, want *cascade.Error) {
	t.Helper()
	for e := err; e != nil; e = errors.Unwrap(e) {
		if e == error(want) {
			if !strings.Contains(err.Error(), want.Msg) {
				t.Fatalf("error %q carries %q but not its message", err, want.Msg)
			}
			return
		}
	}
	t.Fatalf("error %v is not sentinel %q", err, want.Msg)
}

// scanNamespace returns every stored key under prefix in the audit domain.
func scanNamespace(t *testing.T, store provider.Store, prefix string) map[string][]byte {
	t.Helper()
	ctx := context.Background()
	it, err := store.Scan(ctx, namespace, prefix)
	if err != nil {
		t.Fatalf("Scan %q: %v", prefix, err)
	}
	defer func() { _ = it.Close() }()
	out := map[string][]byte{}
	for it.Next(ctx) {
		out[it.Key()] = append([]byte(nil), it.Value()...)
	}
	if err := it.Err(); err != nil {
		t.Fatalf("Scan %q: %v", prefix, err)
	}
	return out
}

// storedEffects decodes every stored record carrying key, by phase.
func storedEffects(t *testing.T, store provider.Store, key string) map[EffectPhase]int {
	t.Helper()
	out := map[EffectPhase]int{}
	for k, raw := range scanNamespace(t, store, recordPrefix) {
		rec, err := decodeRecord(raw)
		if err != nil {
			t.Fatalf("stored record %s: %v", k, err)
		}
		if rec.EffectKey == key {
			out[EffectPhase(rec.EffectPhase)]++
		}
	}
	return out
}

func TestBeginEffectTwiceConflicts(t *testing.T) {
	for _, s := range effectStores {
		t.Run(s.name, func(t *testing.T) {
			ctx := context.Background()
			store := s.open(t)
			el, _ := newEffectLog(t, store)
			if _, err := el.BeginEffect(ctx, effectReq("approval.grant:r-1")); err != nil {
				t.Fatalf("first BeginEffect: %v", err)
			}
			before := scanNamespace(t, store, "")
			h, err := el.BeginEffect(ctx, effectReq("approval.grant:r-1"))
			requireSentinel(t, err, ErrEffectExists)
			if after := scanNamespace(t, store, ""); !reflect.DeepEqual(before, after) {
				t.Fatalf("a refused BeginEffect changed the store:\nbefore %q\nafter  %q", before, after)
			}
			if h != (EffectHandle{}) {
				t.Fatalf("a refused BeginEffect returned handle %+v", h)
			}
			got := storedEffects(t, store, "approval.grant:r-1")
			if got[EffectIntent] != 1 || len(got) != 1 {
				t.Fatalf("stored effect records = %v, want exactly one intent", got)
			}
		})
	}
}

func TestBeginEffectRefusesBadKeys(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	el, _ := newEffectLog(t, store)
	for _, key := range []string{"", "a/b", strings.Repeat("k", 129), "a b", "a\nb", "eff:x\x00"} {
		_, err := el.BeginEffect(ctx, effectReq(key))
		requireSentinel(t, err, ErrInvalidEvent)
		if _, _, serr := el.EffectState(ctx, key); serr == nil {
			t.Fatalf("EffectState(%q) accepted a malformed key", key)
		}
	}
	if _, err := el.BeginEffect(ctx, effectReq(strings.Repeat("k", 128))); err != nil {
		t.Fatalf("a 128-byte key was refused: %v", err)
	}
	if n := len(scanNamespace(t, store, recordPrefix)); n != 1 {
		t.Fatalf("stored %d records, want only the 128-byte key's intent", n)
	}
	if _, err := NewEffectLog(nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("NewEffectLog(nil) = %v, want KindInvalidInput", err)
	}
}

func TestEffectTerminalOnce(t *testing.T) {
	ends := map[string]func(*EffectLog, EffectHandle) (Record, error){
		"confirm": func(el *EffectLog, h EffectHandle) (Record, error) {
			return el.ConfirmEffect(context.Background(), h, "cleared")
		},
		"fail": func(el *EffectLog, h EffectHandle) (Record, error) {
			return el.FailEffect(context.Background(), h, "clipboard owned by another app")
		},
		"unknown": func(el *EffectLog, h EffectHandle) (Record, error) {
			return el.MarkUnknownOutcome(context.Background(), h)
		},
	}
	for _, s := range effectStores {
		for name, end := range ends {
			t.Run(s.name+"/"+name, func(t *testing.T) {
				ctx := context.Background()
				store := s.open(t)
				el, _ := newEffectLog(t, store)
				h, err := el.BeginEffect(ctx, effectReq("clipboard.clear:"+name))
				if err != nil {
					t.Fatalf("BeginEffect: %v", err)
				}
				if _, err := end(el, h); err != nil {
					t.Fatalf("first terminal write: %v", err)
				}
				before := scanNamespace(t, store, "")
				for again, retry := range ends {
					_, err := retry(el, h)
					requireSentinel(t, err, ErrEffectNotPending)
					if after := scanNamespace(t, store, ""); !reflect.DeepEqual(before, after) {
						t.Fatalf("%s after %s changed the store:\nbefore %q\nafter  %q", again, name, before, after)
					}
				}
				if got := storedEffects(t, store, h.Key); got[EffectIntent] != 1 || len(got) != 2 {
					t.Fatalf("stored effect records = %v, want one intent and one terminal", got)
				}
			})
		}
	}
}

func TestEffectTerminalRefusesForeignHandles(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	el, _ := newEffectLog(t, store)
	h, err := el.BeginEffect(ctx, effectReq("node.dial:n-1"))
	if err != nil {
		t.Fatalf("BeginEffect: %v", err)
	}
	for _, forged := range []EffectHandle{
		{Key: "node.dial:never", Kind: h.Kind, IntentSeq: h.IntentSeq, IntentID: h.IntentID},
		{Key: h.Key, Kind: h.Kind, IntentSeq: h.IntentSeq + 1, IntentID: h.IntentID},
		{Key: h.Key, Kind: h.Kind, IntentSeq: h.IntentSeq, IntentID: "01FORGED"},
		{Key: h.Key, Kind: KindVaultAccess, IntentSeq: h.IntentSeq, IntentID: h.IntentID},
	} {
		_, err := el.ConfirmEffect(ctx, forged, "done")
		requireSentinel(t, err, ErrEffectNotPending)
	}
	if phase, _, err := el.EffectState(ctx, h.Key); err != nil || phase != EffectIntent {
		t.Fatalf("EffectState after forged handles = %q, %v; want intent", phase, err)
	}
	h2, err := el.BeginEffect(ctx, effectReq("node.dial:n-0"))
	pending, perr := el.PendingEffects(ctx)
	if err != nil || perr != nil || len(pending) != 2 || pending[0] != h || pending[1] != h2 {
		t.Fatalf("PendingEffects = %+v, %v; want [%+v %+v] oldest first", pending, perr, h, h2)
	}
}

func TestAppendRefusesEffectFields(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	el, log := newEffectLog(t, store)
	h, err := el.BeginEffect(ctx, effectReq("oauth.refresh:acct-1"))
	if err != nil {
		t.Fatalf("BeginEffect: %v", err)
	}
	for _, forge := range []Event{
		{EffectKey: h.Key},
		{EffectPhase: string(EffectConfirmed)},
		{EffectKey: h.Key, EffectPhase: string(EffectConfirmed)},
		{EffectKey: "oauth.refresh:acct-2", EffectPhase: string(EffectIntent)},
	} {
		forge.Kind, forge.Actor, forge.Action = KindExternalEffect, "agent:test", "oauth.refresh"
		_, err := log.Append(ctx, forge)
		requireSentinel(t, err, ErrInvalidEvent)
	}
	if n := len(scanNamespace(t, store, recordPrefix)); n != 1 {
		t.Fatalf("stored %d records, want only the intent", n)
	}
	if phase, _, err := el.EffectState(ctx, h.Key); err != nil || phase != EffectIntent {
		t.Fatalf("EffectState after forged appends = %q, %v; want intent", phase, err)
	}
	if _, found, err := el.EffectState(ctx, "oauth.refresh:acct-2"); err != nil || found {
		t.Fatalf("a refused forged intent is visible: found=%v err=%v", found, err)
	}
}
