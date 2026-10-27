package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// assertDenied checks the refusal text and that the call's last two rows
// are attempt then deny with reason, and that nothing was granted.
func assertDenied(t *testing.T, label string, f *gateFixture, err error, text, reason string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), text) {
		t.Fatalf("%s = %v, want a refusal containing %q", label, err, text)
	}
	recs := f.tap.recs
	n := len(recs)
	if n < 2 || recs[n-2].Kind != audit.KindElevationAttempt || recs[n-1].Kind != audit.KindElevationDeny ||
		!strings.Contains(string(recs[n-1].Explain), `"reason":"`+reason+`"`) {
		t.Fatalf("%s: rows %v, last %s; want attempt then deny %q", label, f.tap.kinds(), lastExplain(f), reason)
	}
	for _, r := range recs {
		if r.Kind == audit.KindElevationGrant {
			t.Fatalf("%s: a grant row was written", label)
		}
	}
}

func lastExplain(f *gateFixture) string {
	if len(f.tap.recs) == 0 {
		return ""
	}
	return string(f.tap.recs[len(f.tap.recs)-1].Explain)
}

// TestEveryLocalRefusalIsAudited: each kind of refusal leaves an attempt row
// and a deny row carrying its reason, and the error names the cause.
func TestEveryLocalRefusalIsAudited(t *testing.T) {
	skipWithoutLocalElevation(t)
	wrongMethod := func(ks elevation.ElevationKeystore, c runtime.Clock, _, h, n string) (rpc.Attestation, error) {
		return signAttestation(ks, c, "node.remove", h, n)
	}
	cases := []struct {
		name, verb, text, reason string
		arrange                  func(f *gateFixture)
	}{
		{"no input", "vault.get", "CASCADE_NO_INPUT", "no_input", func(f *gateFixture) {
			f.gate.getenv = func(k string) string { return map[string]string{"CASCADE_NO_INPUT": "1"}[k] }
		}},
		{"unbound", "vault.get", "no enrolled key", "unbound", func(f *gateFixture) {
			dir := t.TempDir()
			f.gate.dataDir = func() string { return dir }
		}},
		{"unknown verb", "local.vault.get", "not an elevated verb", "not_elevated", nil},
		{"sign error", "vault.get", "fake: presence refused", "sign_failed", func(f *gateFixture) {
			f.ks.sign = func([]byte) ([]byte, error) { return nil, errors.New("fake: presence refused") }
		}},
		{"no signature", "vault.get", "returned no signature", "no_signature", func(f *gateFixture) {
			f.ks.sign = func([]byte) ([]byte, error) { return nil, nil }
		}},
		{"wrong method", "node.revoke", "method does not match", "invalid-input", func(f *gateFixture) { f.gate.attest = wrongMethod }},
	}
	if !elevation.DevkeysBuild() {
		cases = append(cases, struct {
			name, verb, text, reason string
			arrange                  func(f *gateFixture)
		}{"file tier", "vault.get", "custody tier file", "custody_tier", func(f *gateFixture) {
			f.gate.custody = testCustody(t, &refusingFileKey{signingKeystore: f.ks.signingKeystore}, elevation.CustodyFile)
		}})
	}
	for _, tc := range cases {
		f := newGateFixture(t, nil)
		if tc.arrange != nil {
			tc.arrange(f)
		}
		assertDenied(t, tc.name, f, f.gate.Authorize(t.Context(), tc.verb), tc.text, tc.reason)
	}
}

// TestReplayedProofIsDenied: a replayed attestation is refused at the nonce
// claim and the local gate still records the deny.
func TestReplayedProofIsDenied(t *testing.T) {
	skipWithoutLocalElevation(t)
	f := newGateFixture(t, nil)
	var first rpc.Attestation
	f.gate.attest = func(ks elevation.ElevationKeystore, c runtime.Clock, m, h, n string) (rpc.Attestation, error) {
		att, err := signAttestation(ks, c, m, h, n)
		first = att
		return att, err
	}
	if err := f.gate.Authorize(t.Context(), "vault.get"); err != nil {
		t.Fatalf("positive control: %v", err)
	}
	f.gate.attest = func(elevation.ElevationKeystore, runtime.Clock, string, string, string) (rpc.Attestation, error) {
		return first, nil
	}
	err := f.gate.Authorize(t.Context(), "vault.get")
	f.tap.recs = f.tap.recs[3:] // drop the positive control's attempt, attempt, grant
	assertDenied(t, "replay", f, err, "nonce not found", cascade.KindNotFound.String())
}

// TestVaultAccessRecordedBeforeAnyCustodyRead: vault.access is written
// before the broker reads; when it cannot be written nothing is read.
func TestVaultAccessRecordedBeforeAnyCustodyRead(t *testing.T) {
	skipWithoutLocalElevation(t)
	f := newGateFixture(t, nil)
	f.gate.sink = &failingKindWriter{inner: f.tap, kind: audit.KindVaultAccess}
	deps, spy := seededVault(t, f.gate)
	stdout, stderr, err := runVault(t, deps, "", "get", "FAKE_NAME")
	if err == nil || stdout != "" || spy.gets != 0 || f.ks.signs != 0 || !strings.Contains(stderr, "audit_unavailable") {
		t.Fatalf("get with an unrecordable access = (%q, %q, %v) gets=%d signs=%d", stdout, stderr, err, spy.gets, f.ks.signs)
	}
	f.gate.sink = f.tap
	if _, _, err := runVault(t, deps, "", "get", "FAKE_NAME"); err != nil || spy.gets != 1 {
		t.Fatalf("positive control: %v gets=%d", err, spy.gets)
	}
	if f.tap.recs[0].Kind != audit.KindVaultAccess {
		t.Fatalf("rows %v: vault.access must precede the elevation rows and the read", f.tap.kinds())
	}
}

// failingKindWriter fails appends of one kind and forwards the rest.
type failingKindWriter struct {
	inner audit.Writer
	kind  audit.Kind
}

func (w *failingKindWriter) Append(ctx context.Context, e audit.Event) (audit.Record, error) {
	if e.Kind == w.kind {
		return audit.Record{}, errors.New("fake: append refused")
	}
	return w.inner.Append(ctx, e)
}

// AppendGateRow lets the test writers stand in for a gate sink: the row is
// validated and turned into its event exactly as production does.
func (w *tapWriter) AppendGateRow(ctx context.Context, row gateRow) (uint64, error) {
	return gateRowLog{log: w}.AppendGateRow(ctx, row)
}

// AppendGateRow implements gateSink over the failing writer.
func (w *failingKindWriter) AppendGateRow(ctx context.Context, row gateRow) (uint64, error) {
	return gateRowLog{log: w}.AppendGateRow(ctx, row)
}

// TestRefusedVaultGetSelectsNoCustody: a vault get refused for no input,
// an unbound key, a replayed proof or an unrecordable access selects no
// vault custody and reads nothing; the positive control selects once.
func TestRefusedVaultGetSelectsNoCustody(t *testing.T) {
	skipWithoutLocalElevation(t)
	f := newGateFixture(t, nil)
	deps, spy := seededVault(t, f.gate)
	selections, inner := 0, deps.NewCustody
	deps.NewCustody = func() (secrets.Custody, error) { selections++; return inner() }
	get := func() error { _, _, err := runVault(t, deps, "", "get", "FAKE_NAME"); return err }
	refused := func(label string) {
		t.Helper()
		if err := get(); err == nil || selections != 0 || spy.gets != 0 {
			t.Fatalf("%s: get = %v after %d custody selections and %d reads", label, err, selections, spy.gets)
		}
	}
	f.gate.getenv = func(k string) string { return map[string]string{"CASCADE_NO_INPUT": "1"}[k] }
	refused("no input")
	f.gate.getenv = func(string) string { return "" }
	dataDir, unbound := f.gate.dataDir, t.TempDir()
	f.gate.dataDir = func() string { return unbound }
	refused("unbound")
	f.gate.dataDir = dataDir
	f.gate.sink = &failingKindWriter{inner: f.tap, kind: audit.KindVaultAccess}
	refused("unrecordable access")
	f.gate.sink = f.tap
	var first rpc.Attestation
	f.gate.attest = func(ks elevation.ElevationKeystore, c runtime.Clock, m, h, n string) (rpc.Attestation, error) {
		att, err := signAttestation(ks, c, m, h, n)
		first = att
		return att, err
	}
	if err := get(); err != nil || selections != 1 || spy.gets != 1 {
		t.Fatalf("positive control = %v, %d selections, %d reads", err, selections, spy.gets)
	}
	selections, spy.gets = 0, 0
	f.gate.attest = func(elevation.ElevationKeystore, runtime.Clock, string, string, string) (rpc.Attestation, error) {
		return first, nil
	}
	refused("replay")
}
