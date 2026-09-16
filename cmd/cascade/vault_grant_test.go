package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the `cascade vault grant|grants|revoke` contract —
//   which verbs cost elevation and which deliberately do not, that the
//   register never carries a value, and that all three print text rather
//   than a Go struct literal.
// Constraints: every test runs over a temp-dir register through the SAME
//   command tree production mounts; no test reaches the operator's real
//   data directory or keychain.
// SPORT: cli.vault.grant tests (ADD) — P1-E10-W4-S87-T1.

// grantPaths is a runtime.PathProvider rooted in a temp dir, so the grant
// register is written there and never under the operator's profile.
type grantPaths struct{ dir string }

func (p grantPaths) Root() string                         { return p.dir }
func (p grantPaths) ConfigPath() string                   { return p.dir + "/config.toml" }
func (p grantPaths) SocketPath() string                   { return p.dir + "/daemon.sock" }
func (p grantPaths) DataDir() string                      { return p.dir }
func (p grantPaths) LogDir() string                       { return p.dir + "/logs" }
func (p grantPaths) StorageRoot(_ runtime.Profile) string { return p.dir }

// grantDeps builds vault deps whose grant register lives in t.TempDir().
func grantDeps(t *testing.T, gate secrets.ElevationGate) vaultDeps {
	t.Helper()
	deps := testVaultDeps(t, gate, nil)
	deps.Paths = grantPaths{dir: t.TempDir()}
	return deps
}

// issueOneGrant issues a grant and returns its id, failing the test if the
// command refused.
func issueOneGrant(t *testing.T, deps vaultDeps, name string) string {
	t.Helper()
	stdout, _, err := runVault(t, deps, "", "grant", name)
	if err != nil {
		t.Fatalf("grant %s: %v", name, err)
	}
	fields := strings.Fields(stdout)
	if len(fields) == 0 {
		t.Fatalf("grant printed nothing: %q", stdout)
	}
	return fields[0]
}

// TestIssuingAGrantCostsElevation is the rule the whole design rests on:
// minting permission to read a secret without a human present must cost
// exactly what reading the secret costs. A grant issuable without presence
// would be a way around the elevation gate rather than a use of it.
func TestIssuingAGrantCostsElevation(t *testing.T) {
	deps := grantDeps(t, refusingGate{})
	_, _, err := runVault(t, deps, "", "grant", "PROVIDER_A_KEY")
	if !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Fatalf("err = %v, want KindElevationRequired", err)
	}
	// And the refusal must have happened BEFORE anything was written: a
	// register that already holds the grant would mean the gate ran after
	// the effect it guards.
	stdout, _, listErr := runVault(t, deps, "", "grants")
	if listErr != nil {
		t.Fatalf("grants: %v", listErr)
	}
	if strings.Contains(stdout, "PROVIDER_A_KEY") {
		t.Errorf("a refused grant was still written: %q", stdout)
	}
}

// TestIssuingAGrantWithNoGateRefuses covers the nil gate. Nil is a refusal
// rather than an absence of policy — the same rule secrets.Broker applies.
func TestIssuingAGrantWithNoGateRefuses(t *testing.T) {
	deps := grantDeps(t, nil)
	deps.Gate = nil
	_, _, err := runVault(t, deps, "", "grant", "PROVIDER_A_KEY")
	if !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Fatalf("err = %v, want KindElevationRequired", err)
	}
}

// TestRevokingIsDeliberatelyNotElevated holds the asymmetry stated in the
// command's own Long text. Revoking only ever removes authority, and a
// revoke an operator could be locked out of would be worse than no revoke:
// the grant would outlive the operator's ability to withdraw it.
func TestRevokingIsDeliberatelyNotElevated(t *testing.T) {
	issuing := grantDeps(t, okGate{})
	id := issueOneGrant(t, issuing, "PROVIDER_A_KEY")

	// Same register, but presence can no longer be proved.
	locked := issuing
	locked.Gate = refusingGate{}
	if _, _, err := runVault(t, locked, "", "revoke", id); err != nil {
		t.Fatalf("revoke behind a refusing gate: %v, want it to succeed", err)
	}
	if _, _, err := runVault(t, locked, "", "grants"); err != nil {
		t.Fatalf("grants behind a refusing gate: %v, want it to succeed", err)
	}
}

// TestRevocationIsVisibleInTheRegister is the property that separates a
// revocation from a deletion: the record stays, marked, so an operator can
// still see that the authority once existed.
func TestRevocationIsVisibleInTheRegister(t *testing.T) {
	deps := grantDeps(t, okGate{})
	id := issueOneGrant(t, deps, "PROVIDER_A_KEY")

	before, _, err := runVault(t, deps, "", "grants")
	if err != nil {
		t.Fatalf("grants: %v", err)
	}
	if !strings.Contains(before, "live") {
		t.Errorf("a fresh grant renders as %q, want it marked live", before)
	}
	if _, _, err := runVault(t, deps, "", "revoke", id); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	after, _, err := runVault(t, deps, "", "grants")
	if err != nil {
		t.Fatalf("grants after revoke: %v", err)
	}
	if !strings.Contains(after, id) {
		t.Errorf("the revoked grant vanished from the register: %q", after)
	}
	if !strings.Contains(after, "revoked") {
		t.Errorf("the register renders %q, want the grant marked revoked", after)
	}
}

// TestRevokingAnUnknownIDIsNotFound keeps a typo from reporting success.
// An operator who mistypes an id and is told nothing would believe an
// authority was withdrawn that is still live.
func TestRevokingAnUnknownIDIsNotFound(t *testing.T) {
	deps := grantDeps(t, okGate{})
	issueOneGrant(t, deps, "PROVIDER_A_KEY")
	_, _, err := runVault(t, deps, "", "revoke", "0000000000000000")
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("err = %v, want KindNotFound", err)
	}
}

// TestTheGrantCLINeverEmitsAValue is the standing rule at the CLI surface.
// The register is structurally incapable of holding one, and this asserts
// the command tree does not reach past it to the vault.
func TestTheGrantCLINeverEmitsAValue(t *testing.T) {
	deps := grantDeps(t, okGate{})
	const value = "s3cr3t-value"
	if _, _, err := runVault(t, deps, value+"\n", "set", "PROVIDER_A_KEY"); err != nil {
		t.Fatalf("set: %v", err)
	}
	issueOneGrant(t, deps, "PROVIDER_A_KEY")
	for _, args := range [][]string{{"grants"}, {"grants", "--json"}} {
		stdout, stderr, err := runVault(t, deps, "", args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if strings.Contains(stdout, value) || strings.Contains(stderr, value) {
			t.Errorf("%v emitted the secret value", args)
		}
		if !strings.Contains(stdout, "PROVIDER_A_KEY") {
			t.Errorf("%v printed %q, want the key name", args, stdout)
		}
	}
}

// TestAnEmptyRegisterSaysSo is the raw-struct-output defect the W-3 gate
// recorded against `vault list` and `recall index rebuild`. Without the
// String methods the writer falls back to Go's default formatting and an
// operator with no grants is shown `[]`.
func TestAnEmptyRegisterSaysSo(t *testing.T) {
	deps := grantDeps(t, okGate{})
	stdout, _, err := runVault(t, deps, "", "grants")
	if err != nil {
		t.Fatalf("grants: %v", err)
	}
	if !strings.Contains(stdout, "no standing grants") {
		t.Errorf("stdout = %q, want a sentence rather than an empty slice", stdout)
	}
	if strings.Contains(stdout, "[]") || strings.Contains(stdout, "{") {
		t.Errorf("stdout = %q, want text rather than a Go literal", stdout)
	}
}

// TestGrantLinesCarryEveryFieldAnOperatorNeeds asserts the rendering
// directly, so a field silently dropped from String is caught here rather
// than by someone reading a listing that no longer says when a grant ends.
func TestGrantLinesCarryEveryFieldAnOperatorNeeds(t *testing.T) {
	view := vaultGrantView{
		ID: "3f9a1c0b2d4e5f60", KeyRef: "PROVIDER_A_KEY", Verb: secrets.VerbGet,
		ExpiresAt: "2026-09-23T12:00:00Z",
	}
	line := view.String()
	for _, want := range []string{view.ID, view.KeyRef, view.Verb, view.ExpiresAt, "live"} {
		if !strings.Contains(line, want) {
			t.Errorf("rendered line %q is missing %q", line, want)
		}
	}
	view.Revoked = true
	if got := view.String(); !strings.Contains(got, "revoked") || strings.Contains(got, "live") {
		t.Errorf("a revoked grant renders as %q, want it marked revoked", got)
	}
	list := vaultGrantList{view, view}
	if got := strings.Count(list.String(), "\n"); got != 1 {
		t.Errorf("two grants rendered across %d newlines, want one line each", got+1)
	}
}

// TestTheGrantRegisterRefusesWithNoDataDirectory covers the two ways the
// data directory can be unresolvable. Neither may fall back to the process
// working directory: a register written there would be invisible to the
// daemon that is supposed to read it.
func TestTheGrantRegisterRefusesWithNoDataDirectory(t *testing.T) {
	for name, deps := range map[string]vaultDeps{
		"nil provider":   {},
		"empty data dir": {Paths: grantPaths{dir: ""}},
	} {
		if _, err := vaultGrants(deps); !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Errorf("%s: err = %v, want KindUnavailable", name, err)
		}
	}
}

// TestTheIssuedGrantIsTheOneTheDaemonReads closes the loop the CLI exists
// for: a grant minted here must authorise the daemon's own read path, not
// merely appear in a listing. Asserted against secrets.Grants directly,
// which is the seam internal/providers/dispatch consults.
func TestTheIssuedGrantIsTheOneTheDaemonReads(t *testing.T) {
	deps := grantDeps(t, okGate{})
	id := issueOneGrant(t, deps, "PROVIDER_A_KEY")

	grants, err := vaultGrants(deps)
	if err != nil {
		t.Fatalf("open the register the daemon reads: %v", err)
	}
	ctx := context.Background()
	if _, ok, _ := grants.Lookup(ctx, "PROVIDER_A_KEY", secrets.VerbGet); !ok {
		t.Fatal("the issued grant does not authorise the daemon's read")
	}
	if _, ok, _ := grants.Lookup(ctx, "PROVIDER_B_KEY", secrets.VerbGet); ok {
		t.Error("a grant for one key authorised another")
	}
	if _, _, err := runVault(t, deps, "", "revoke", id); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	// Reopened, because the daemon reads the register per request rather
	// than holding it: that is what makes a revoke effective on the next
	// read instead of the next restart.
	reopened, err := vaultGrants(deps)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := reopened.Lookup(ctx, "PROVIDER_A_KEY", secrets.VerbGet); ok {
		t.Error("a revoked grant still authorises the daemon's read")
	}
}

// TestTheDefaultTTLIsBoundedAndClamped covers the flag. A grant with no
// end is the standing exemption R-14.243 rejected, so both the default and
// an operator-supplied excess must land inside the ceiling.
func TestTheDefaultTTLIsBoundedAndClamped(t *testing.T) {
	if secrets.DefaultGrantTTL <= 0 || secrets.DefaultGrantTTL > secrets.MaxGrantTTL {
		t.Fatalf("default TTL %v is not inside the ceiling %v", secrets.DefaultGrantTTL, secrets.MaxGrantTTL)
	}
	deps := grantDeps(t, okGate{})
	if _, _, err := runVault(t, deps, "", "grant", "--ttl", "8760h", "PROVIDER_A_KEY"); err != nil {
		t.Fatalf("grant with an excessive ttl: %v", err)
	}
	grants, err := vaultGrants(deps)
	if err != nil {
		t.Fatal(err)
	}
	all, err := grants.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("%d grants, want 1", len(all))
	}
	if ceiling := time.Now().Add(secrets.MaxGrantTTL).Add(time.Minute); all[0].ExpiresAt.After(ceiling) {
		t.Errorf("expiry %v is past the ceiling %v; a year-long grant was accepted verbatim",
			all[0].ExpiresAt, ceiling)
	}
}
