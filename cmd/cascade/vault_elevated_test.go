package main

import (
	"bytes"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestVaultCLIGetRequiresElevation is the acceptance criterion: without an
// elevation session, get and rotate emit ELEVATION_REQUIRED as a typed
// error (never a panic) and read nothing.
func TestVaultCLIGetRequiresElevation(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, map[string]string{"CASCADE_NO_INPUT": "1"})
	if _, _, err := runVault(t, deps, "s3cr3t\n", "set", "TOKEN"); err != nil {
		t.Fatalf("set: %v", err)
	}
	deps.Gate = refusingGate{}

	stdout, stderr, err := runVault(t, deps, "", "get", "TOKEN")
	if !isCLIKind(err, cascade.KindElevationRequired) {
		t.Fatalf("get without elevation = %v, want ELEVATION_REQUIRED", err)
	}
	if cascade.KindElevationRequired.ExitCode() != cascade.ExitElevationRequired {
		t.Fatal("the refusal does not map to the elevation-required exit status")
	}
	if strings.Contains(stdout+stderr+err.Error(), "s3cr3t") {
		t.Fatal("the refusal leaked the secret value")
	}

	stdout, stderr, err = runVault(t, deps, "new\n", "rotate", "TOKEN")
	if !isCLIKind(err, cascade.KindElevationRequired) {
		t.Fatalf("rotate without elevation = %v, want ELEVATION_REQUIRED", err)
	}
	if strings.Contains(stdout+stderr, "s3cr3t") {
		t.Fatal("the rotate refusal leaked the stored value")
	}

	// The refused rotate must not have written anything.
	deps.Gate = okGate{}
	stdout, _, err = runVault(t, deps, "", "get", "TOKEN")
	if err != nil {
		t.Fatalf("get after a refused rotate: %v", err)
	}
	if stdout != "s3cr3t" {
		t.Fatalf("the refused rotate changed the stored value to %q", stdout)
	}
}

// TestVaultCLIGetElevationRequiredMessageCasing is
// DEFECT-elevation-required-case-mismatch.md's regression test. It drives
// `cascade vault get` through the real CLI entry point AND the real
// production elevationGate (newElevationGate, wired the same way
// productionVaultDeps wires it — a keystore/backend pair that cannot prove
// local presence, so policy.ErrElevationRequired is the one that actually
// builds the message; the test-only refusingGate fake used elsewhere in
// this file is deliberately NOT used here, since its hardcoded message
// contains the literal uppercase substring this test must tell apart from
// the real one). The error is then rendered through the SAME production
// path main.go uses (internal/output.Writer.Fail over os.Stderr) so the
// literal bytes asserted here are the literal bytes a shell pipeline sees.
//
// pkg/cascade/kinds.go documents the kind strings as lowercase-hyphenated
// and part of the frozen wire contract ("must never change once a kind
// ships"), so the fix landed in the ticket's check pattern
// (T-7.yaml, ELEVATION_REQUIRED -> elevation-required), never in the CLI:
// this test pins the CLI's existing, correct casing and would catch either
// a future accidental uppercase rename OR a regression back to it.
func TestVaultCLIGetElevationRequiredMessageCasing(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, map[string]string{"CASCADE_NO_INPUT": "1"})
	if _, _, err := runVault(t, deps, "s3cr3t\n", "set", "CASCADE_GATE_PROBE"); err != nil {
		t.Fatalf("set: %v", err)
	}
	deps.Gate = newElevationGate(
		testCustody(t, availableKeystore{}, elevation.CustodyPlatform),
		func() string { return t.TempDir() }, &tapWriter{inner: audit.New(storetest.NewMemStore(), runtime.NewSystemClock(), nil)},
		runtime.NewSystemClock(),
		func(string) string { return "" },
	)

	_, _, err := runVault(t, deps, "", "get", "CASCADE_GATE_PROBE")
	if !isCLIKind(err, cascade.KindElevationRequired) {
		t.Fatalf("get with no session = %v, want ELEVATION_REQUIRED", err)
	}

	var stderr bytes.Buffer
	output.New(&bytes.Buffer{}, &stderr, false, false, false, true).Fail(err)
	rendered := stderr.String()

	if !strings.Contains(rendered, "elevation-required") {
		t.Fatalf("rendered CLI error %q does not contain the taxonomy's lowercase-hyphenated kind", rendered)
	}
	if strings.Contains(rendered, "ELEVATION_REQUIRED") {
		t.Fatalf("rendered CLI error %q regressed to the uppercase literal", rendered)
	}
}

func TestVaultCLIRotate(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, map[string]string{"CASCADE_NO_INPUT": "1"})
	if _, _, err := runVault(t, deps, "old\n", "set", "TOKEN"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, _, err := runVault(t, deps, "new\n", "rotate", "TOKEN"); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	stdout, _, err := runVault(t, deps, "", "get", "TOKEN")
	if err != nil || stdout != "new" {
		t.Fatalf("get after rotate = %q, %v", stdout, err)
	}
	// Rotating a name that is not stored is a not-found refusal, never a
	// silent create.
	if _, _, err := runVault(t, deps, "v\n", "rotate", "ABSENT"); !isCLIKind(err, cascade.KindNotFound) {
		t.Fatalf("rotating an absent name = %v", err)
	}
	names, _, err := runVault(t, deps, "", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Contains(names, "ABSENT") {
		t.Fatal("a refused rotate created the secret")
	}
}

func TestVaultCLIGetAbsent(t *testing.T) {
	deps := testVaultDeps(t, okGate{}, nil)
	if _, _, err := runVault(t, deps, "", "get", "ABSENT"); !isCLIKind(err, cascade.KindNotFound) {
		t.Fatalf("get of an absent name = %v", err)
	}
}

// TestElevationGateFailsClosed drives the production gate directly. Each
// case is a state in which local presence cannot be proved, and every one
// must refuse.
func TestElevationGateFailsClosed(t *testing.T) {
	clock := runtime.NewSystemClock()
	noEnv := func(string) string { return "" }
	enrolledDir := func(ks elevation.ElevationKeystore) func() string {
		dir := t.TempDir()
		if pub, err := ks.PubKeyB64(); err == nil && pub != "" {
			if _, err := elevation.NewElevationTrustStore(elevation.NewFileBackend(dir), clock).Enroll(pub); err != nil {
				t.Fatal(err)
			}
		}
		return func() string { return dir }
	}
	signer := newSigningKeystore(t)
	log := &tapWriter{inner: audit.New(storetest.NewMemStore(), clock, nil)}
	cases := map[string]*elevationGate{
		"no custody and no data dir": newElevationGate(nil, nil, log, clock, noEnv),
		"keystore returns nil": newElevationGate(
			testCustody(t, nil, elevation.CustodyPlatform), enrolledDir(signer), log, clock, noEnv),
		"no input allowed": newElevationGate(
			testCustody(t, signer, elevation.CustodyPlatform), enrolledDir(signer), log, clock,
			func(k string) string {
				if k == "CASCADE_NO_INPUT" {
					return "1"
				}
				return ""
			}),
		"no authenticator": newElevationGate(
			testCustody(t, unavailableKeystore{}, elevation.CustodyPlatform), enrolledDir(signer), log, clock, noEnv),
		"authenticator but not enrolled": newElevationGate(
			testCustody(t, signer, elevation.CustodyPlatform), func() string { return t.TempDir() }, log, clock, noEnv),
		"enrolled under another key": newElevationGate(
			testCustody(t, signer, elevation.CustodyPlatform), enrolledDir(newSigningKeystore(t)), log, clock, noEnv),
		"no audit writer": newElevationGate(
			testCustody(t, signer, elevation.CustodyPlatform), enrolledDir(signer), nil, clock, noEnv),
		"empty data dir": newElevationGate(
			testCustody(t, signer, elevation.CustodyPlatform), func() string { return "" }, log, clock, noEnv),
	}
	for label, gate := range cases {
		for _, verb := range []string{"vault.get", "vault.rotate"} {
			if err := gate.Authorize(t.Context(), verb); err == nil {
				t.Fatalf("%s: %s was authorised with no proof of local presence", label, verb)
			}
		}
	}
}

// TestElevationGateAllowsWhenBothPreconditionsHold is the negative control:
// the gate must not refuse everything unconditionally, or the tests above
// would pass against a gate that is simply broken. A verb that is not
// elevated is refused: the local gate only ever authorises table verbs.
func TestElevationGateAllowsWhenBothPreconditionsHold(t *testing.T) {
	skipWithoutLocalElevation(t)
	f := newGateFixture(t, nil)
	if err := f.gate.Authorize(t.Context(), "vault.get"); err != nil {
		t.Fatalf("a fully-enrolled host was refused: %v", err)
	}
	if err := f.gate.Authorize(t.Context(), "vault.list"); !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Fatalf("a non-elevated verb = %v, want ELEVATION_REQUIRED", err)
	}
}

type availableKeystore struct{}

func (availableKeystore) GenerateKey() error          { return nil }
func (availableKeystore) PubKeyB64() (string, error)  { return "", nil }
func (availableKeystore) Sign([]byte) ([]byte, error) { return nil, nil }
func (availableKeystore) IsAvailable() bool           { return true }
func (availableKeystore) Tier() elevation.StorageTier { return elevation.TierOSKeychain }

type unavailableKeystore struct{ availableKeystore }

func (unavailableKeystore) IsAvailable() bool { return false }

func TestVaultGateRefusesOnFileTier(t *testing.T) {
	k := &refusingFileKey{signingKeystore: newSigningKeystore(t)}
	if elevation.DevkeysBuild() {
		t.Skip("a devkeys build lets file custody sign by design")
	}
	gate := newElevationGate(testCustody(t, k, elevation.CustodyFile), func() string { return t.TempDir() },
		&tapWriter{inner: audit.New(storetest.NewMemStore(), runtime.NewSystemClock(), nil)}, runtime.NewSystemClock(), func(string) string { return "" })
	for _, verb := range []string{"vault.get", "vault.rotate", "vault.grant", "node.enroll", "node.remove", "provider.add"} {
		err := gate.Authorize(t.Context(), verb)
		if tier, ok := elevation.CustodyTierOf(err); !ok || tier != elevation.CustodyFile || k.signs != 0 {
			t.Fatalf("%s: %v signs=%d", verb, err, k.signs)
		}
	}
}

// TestVaultGrantRequiresSignature drives `vault grant` through the
// production signed gate: a signer that returns no signature issues no
// grant; a real signature issues one, audited under the read verb.
func TestVaultGrantRequiresSignature(t *testing.T) {
	skipWithoutLocalElevation(t)
	f := newGateFixture(t, nil)
	f.ks.sign = func([]byte) ([]byte, error) { return nil, nil }
	deps := grantDeps(t, f.gate)
	if _, _, err := runVault(t, deps, "", "grant", "provider.fake.key"); !isCLIKind(err, cascade.KindElevationRequired) {
		t.Fatalf("grant with no signature = %v, want ELEVATION_REQUIRED", err)
	}
	if listed, _, err := runVault(t, deps, "", "grants"); err != nil || strings.Contains(listed, "provider.fake.key") {
		t.Fatalf("a refused grant was issued: %q %v", listed, err)
	}
	f.ks.sign = nil
	id := issueOneGrant(t, deps, "provider.fake.key")
	if id == "" || f.ks.signs != 2 {
		t.Fatalf("grant id %q after %d signatures", id, f.ks.signs)
	}
	rows := f.tap.kinds()
	if len(rows) == 0 || rows[len(rows)-1] != "elevation.grant" || f.tap.recs[len(rows)-1].Action != "vault.get" {
		t.Fatalf("audit rows %v; the grant must be recorded under vault.get", rows)
	}
}

func TestLocalGateUnknownVerbRefuses(t *testing.T) {
	f := newGateFixture(t, nil)
	for _, verb := range []string{"local.vault.get", "node.git_gate_install"} {
		err := f.gate.Authorize(t.Context(), verb)
		if !cascade.HasKind(err, cascade.KindElevationRequired) || !strings.Contains(err.Error(), "not an elevated verb") {
			t.Fatalf("%s = %v, want the not-an-elevated-verb refusal", verb, err)
		}
	}
	if f.ks.signs != 0 || strings.Contains(strings.Join(f.tap.kinds(), ","), "grant") {
		t.Fatalf("an unknown verb signed %d times and wrote rows %v", f.ks.signs, f.tap.kinds())
	}
}

func TestLocalGateFileTierRefuses(t *testing.T) {
	if elevation.DevkeysBuild() {
		t.Skip("a devkeys build lets file custody sign by design")
	}
	f := newGateFixture(t, nil)
	k := &refusingFileKey{signingKeystore: f.ks.signingKeystore}
	f.gate.custody = testCustody(t, k, elevation.CustodyFile)
	for _, verb := range []string{"vault.get", "node.enroll"} {
		err := f.gate.Authorize(t.Context(), verb)
		if tier, ok := elevation.CustodyTierOf(err); !ok || tier != elevation.CustodyFile || k.signs != 0 {
			t.Fatalf("%s: %v (signs %d), want the typed file-tier refusal", verb, err, k.signs)
		}
	}
	if strings.Join(f.tap.kinds(), ",") != "elevation.attempt,elevation.deny,elevation.attempt,elevation.deny" {
		t.Fatalf("a tier refusal wrote rows %v, want attempt and deny per verb", f.tap.kinds())
	}
}

func mustPub(t *testing.T, ks elevation.ElevationKeystore) string {
	t.Helper()
	pub, err := ks.PubKeyB64()
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func skipWithoutLocalElevation(t *testing.T) {
	t.Helper()
	if goruntime.GOOS == "windows" {
		t.Skip("windows is tier 2: the middleware refuses every elevated verb before a challenge")
	}
}
