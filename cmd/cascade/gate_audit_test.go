package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const fakeHash = "0000000000000000000000000000000000000000000000000000000000000000"

// TestGateRowVocabulary: only the closed vocabulary validates.
func TestGateRowVocabulary(t *testing.T) {
	good := []gateRow{
		{Kind: "elevation.attempt", Verb: "vault.get", ParamsHash: fakeHash},
		{Kind: "elevation.deny", Verb: "local.vault.get", Reason: "not_elevated", ParamsHash: fakeHash},
		{Kind: "elevation.deny", Verb: "vault.get", Reason: "not-found", ParamsHash: fakeHash},
		{Kind: "vault.access", Verb: "vault.get", Name: "FAKE_NAME", ParamsHash: fakeHash},
	}
	for _, r := range good {
		if err := r.validate(); err != nil {
			t.Fatalf("%+v refused: %v", r, err)
		}
	}
	bad := map[string]gateRow{
		"forged kind":         {Kind: "policy.decide", Verb: "vault.get", ParamsHash: fakeHash},
		"free text verb":      {Kind: "elevation.attempt", Verb: "vault get now", ParamsHash: fakeHash},
		"free text reason":    {Kind: "elevation.deny", Verb: "vault.get", Reason: "the operator said so", ParamsHash: fakeHash},
		"reason on a grant":   {Kind: "elevation.grant", Verb: "vault.get", Reason: "no_input", ParamsHash: fakeHash},
		"name off vault rows": {Kind: "elevation.attempt", Verb: "vault.get", Name: "FAKE_NAME", ParamsHash: fakeHash},
		"name with spaces":    {Kind: "vault.access", Verb: "vault.get", Name: "a value with spaces", ParamsHash: fakeHash},
		"non-hex hash":        {Kind: "elevation.attempt", Verb: "vault.get", ParamsHash: strings.Repeat("z", 64)},
	}
	for label, r := range bad {
		if err := r.validate(); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s accepted: %v", label, err)
		}
	}
}

// TestGateAppendHandlerBounds: unknown fields, trailing data, oversize
// params and forged kinds are refused; the minute window is enforced; an
// accepted row is acknowledged with its committed sequence number.
func TestGateAppendHandlerBounds(t *testing.T) {
	clock := runtime.NewFixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	h := gateAppendHandler(gateRowLog{log: audit.New(storetest.NewMemStore(), clock, nil)}, clock)
	row := `{"kind":"elevation.attempt","verb":"vault.get","params_hash":"` + fakeHash + `"}`
	for label, raw := range map[string]string{
		"unknown field": `{"kind":"elevation.attempt","verb":"vault.get","params_hash":"` + fakeHash + `","explain":"x"}`,
		"trailing data": row + row,
		"oversize":      `{"kind":"` + strings.Repeat("a", gateRowMaxBytes) + `"}`,
		"forged kind":   strings.Replace(row, "elevation.attempt", "policy.decide", 1),
	} {
		if _, err := h(t.Context(), json.RawMessage(raw)); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s = %v, want invalid input", label, err)
		}
	}
	// A burst of 600 at one instant: only the bucket's burst lands.
	accepted := 0
	for i := 0; i < 600; i++ {
		ack, err := h(t.Context(), json.RawMessage(row))
		switch {
		case err == nil:
			accepted++
			if ack.(gateAppendAck).Seq != uint64(accepted) {
				t.Fatalf("row %d acknowledged as %v", accepted, ack)
			}
		case !cascade.HasKind(err, cascade.KindQuotaExhausted):
			t.Fatalf("burst row = %v", err)
		}
	}
	if accepted != 20 || gateAppendBurst > 20 {
		t.Fatalf("a burst of 600 landed %d rows (burst %d), want 20", accepted, gateAppendBurst)
	}
	// A sustained legitimate rate (one row every 100ms) always lands.
	for i := 0; i < 100; i++ {
		clock.Advance(100 * time.Millisecond)
		if _, err := h(t.Context(), json.RawMessage(row)); err != nil {
			t.Fatalf("sustained row %d = %v", i, err)
		}
	}
}

// TestGateRowsNeverCarryDaemonProvenance: a row cannot name an actor or a
// source (unknown fields are refused), and every appended row is marked
// as an unverified CLI row, never as the daemon's own elevation row.
func TestGateRowsNeverCarryDaemonProvenance(t *testing.T) {
	clock := runtime.NewFixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	tap := &tapWriter{inner: audit.New(storetest.NewMemStore(), clock, nil)}
	h := gateAppendHandler(gateRowLog{log: tap}, clock)
	base := `"kind":"elevation.grant","verb":"vault.get","params_hash":"` + fakeHash + `"`
	for _, forged := range []string{`{` + base + `,"actor":"elevation"}`, `{` + base + `,"source":"daemon"}`} {
		if _, err := h(t.Context(), json.RawMessage(forged)); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("forged provenance %s = %v", forged, err)
		}
	}
	for _, row := range []string{`{` + base + `}`, `{"kind":"vault.access","verb":"vault.get","name":"FAKE_NAME","params_hash":"` + fakeHash + `"}`} {
		if _, err := h(t.Context(), json.RawMessage(row)); err != nil {
			t.Fatalf("row %s = %v", row, err)
		}
	}
	for _, rec := range tap.recs {
		if rec.Actor == "elevation" || !strings.Contains(string(rec.Explain), `"source":"cli-gate"`) {
			t.Fatalf("gate row %s %s carries the daemon's provenance or lacks cli-gate", rec.Actor, rec.Explain)
		}
	}
}

// countProcessVault counts every read of the process vault custody.
func countProcessVault(t *testing.T) *int {
	t.Helper()
	resetProcessFirewall(t)
	reads := 0
	prev := selectProcessCustody
	selectProcessCustody = func(cfg secrets.Config) (secrets.Custody, error) {
		c, err := prev(cfg)
		return countingVault{Custody: c, reads: &reads}, err
	}
	return &reads
}

type countingVault struct {
	secrets.Custody
	reads *int
}

func (v countingVault) Get(ctx context.Context, name string) ([]byte, error) {
	*v.reads++
	return v.Custody.Get(ctx, name)
}

func (v countingVault) List(ctx context.Context) ([]string, error) {
	*v.reads++
	return v.Custody.List(ctx)
}

// TestGateRefusalsReadNoVault: with the production sink writing straight to
// the store, a refused call (no input, replay, missing custody, audit
// failure) reads no vault, and the no-input refusal opens no custody.
func TestGateRefusalsReadNoVault(t *testing.T) {
	skipWithoutLocalElevation(t)
	reads := countProcessVault(t)
	f := newGateFixture(t, nil)
	f.gate.sink = &autoGateSink{paths: fakeDaemonPaths{root: t.TempDir()}, clock: runtime.NewSystemClock()}
	f.gate.getenv = func(k string) string { return map[string]string{"CASCADE_NO_INPUT": "1"}[k] }
	if err := f.gate.Authorize(t.Context(), "vault.get"); err == nil || f.custodys != 0 || *reads != 0 {
		t.Fatalf("no input = %v; custody calls %d, vault reads %d", err, f.custodys, *reads)
	}
	f.gate.getenv = func(string) string { return "" }
	var first rpc.Attestation
	f.gate.attest = func(ks elevation.ElevationKeystore, c runtime.Clock, m, h, n string) (rpc.Attestation, error) {
		att, err := signAttestation(ks, c, m, h, n)
		first = att
		return att, err
	}
	if err := f.gate.Authorize(t.Context(), "vault.get"); err != nil {
		t.Fatalf("positive control over the store sink: %v", err)
	}
	f.gate.attest = func(elevation.ElevationKeystore, runtime.Clock, string, string, string) (rpc.Attestation, error) {
		return first, nil
	}
	if err := f.gate.Authorize(t.Context(), "vault.get"); err == nil {
		t.Fatal("replay granted")
	}
	f.gate.attest = nil
	selectCustody := f.gate.custody
	f.gate.custody = func() elevation.Custody { return elevation.Custody{} }
	if err := f.gate.Authorize(t.Context(), "vault.get"); err == nil {
		t.Fatal("missing custody granted")
	}
	// A real audit-sink failure, with a custody that would otherwise grant.
	f.gate.custody = selectCustody
	f.gate.sink = &autoGateSink{dial: func() (gateCall, error) { return nil, errors.New("fake: audit sink down") }}
	if err := f.gate.Authorize(t.Context(), "vault.get"); err == nil || !strings.Contains(err.Error(), "fake: audit sink down") {
		t.Fatalf("an unavailable audit sink = %v", err)
	}
	if *reads != 0 {
		t.Fatalf("refusals read the vault %d times", *reads)
	}
}

// refusingWriteGate refuses every Authorize and counts them.
type refusingWriteGate struct{ calls *int }

func (g refusingWriteGate) Authorize(context.Context, string) error {
	*g.calls++
	return cascade.New(cascade.KindElevationRequired, "fake: refused")
}

// TestElevatedWriteStoreGatesEveryWrite: Put, Delete and Tx each refuse
// without a grant and leave the store untouched; reads pass; one answer
// per call.
func TestElevatedWriteStoreGatesEveryWrite(t *testing.T) {
	inner := storetest.NewMemStore()
	ctx := t.Context()
	if err := inner.Put(ctx, "ns", "k", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s := &elevatedWriteStore{Store: inner, gate: refusingWriteGate{calls: &calls}, verb: "plugin.add"}
	if err := s.Put(ctx, "ns", "k", []byte("v2")); !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Fatalf("Put = %v", err)
	}
	if err := s.Delete(ctx, "ns", "k"); !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Fatalf("Delete = %v", err)
	}
	ran := false
	if err := s.Tx(ctx, func(context.Context, provider.Tx) error { ran = true; return nil }); !cascade.HasKind(err, cascade.KindElevationRequired) || ran {
		t.Fatalf("Tx = %v, ran %v", err, ran)
	}
	if v, err := s.Get(ctx, "ns", "k"); err != nil || string(v) != "v1" || calls != 1 {
		t.Fatalf("store after refusals = %q %v, gate asked %d times", v, err, calls)
	}
	for name, op := range map[string]func(*elevatedWriteStore) error{
		"Delete": func(s *elevatedWriteStore) error { return s.Delete(ctx, "ns", "k") },
		"Tx": func(s *elevatedWriteStore) error {
			return s.Tx(ctx, func(context.Context, provider.Tx) error { return nil })
		},
	} {
		fresh := &elevatedWriteStore{Store: inner, gate: refusingWriteGate{calls: &calls}, verb: "plugin.add"}
		if err := op(fresh); err == nil {
			t.Fatalf("%s as the first write was admitted", name)
		}
	}
}

// TestDaemonSinkNeedsAnAckAndNeverFallsBack: a daemon reply without a
// committed sequence number refuses, and a daemon that answers with an
// error (rather than not listening) refuses without touching the store.
func TestDaemonSinkNeedsAnAckAndNeverFallsBack(t *testing.T) {
	row := gateRow{Kind: "elevation.attempt", Verb: "vault.get", ParamsHash: fakeHash}
	direct := 0
	openDirect := func(context.Context) (gateSink, error) {
		direct++
		return gateRowLog{log: audit.New(storetest.NewMemStore(), runtime.NewSystemClock(), nil)}, nil
	}
	silent := &autoGateSink{openDirect: openDirect, dial: func() (gateCall, error) {
		return func(context.Context, string, any, any) error { return nil }, nil
	}}
	if _, err := silent.AppendGateRow(t.Context(), row); err == nil || !strings.Contains(err.Error(), "did not acknowledge") {
		t.Fatalf("an unacknowledged row = %v", err)
	}
	failing := &autoGateSink{openDirect: openDirect, dial: func() (gateCall, error) {
		return func(context.Context, string, any, any) error {
			return cascade.New(cascade.KindUnavailable, "fake: daemon refused the row")
		}, nil
	}}
	if _, err := failing.AppendGateRow(t.Context(), row); err == nil || direct != 0 {
		t.Fatalf("a daemon error = %v, direct opens %d", err, direct)
	}
	// No daemon listening: the real client dial fails and the row goes to
	// the store directly.
	listening := &autoGateSink{paths: fakeDaemonPaths{root: shortTempDir(t)}, clock: runtime.NewSystemClock(), openDirect: openDirect}
	if seq, err := listening.AppendGateRow(t.Context(), row); err != nil || seq == 0 || direct != 1 {
		t.Fatalf("no daemon listening = %d %v, direct opens %d", seq, err, direct)
	}
}
