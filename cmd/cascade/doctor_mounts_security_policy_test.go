// Purpose: tests for the policy, approval_key, egress_audit_only and
//
//	caller_principal doctor checks, and that the real registry mounts all six.
//
// Constraints: the vault is a temp file vault reached through the guarded
//
//	read-only selector; no host keychain, no timing, no mode-bit dependence.
//
// SPORT: cmd/cascade doctor security mounts/ADDED (tests, P1-SEC-12).
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/policy"
	internalruntime "github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/provider"
)

func writePolicyConfig(t *testing.T, paths internalruntime.PathProvider, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(paths.Root(), "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatalf("config: %v", err)
	}
}

func TestDoctorPolicyMalformedWarns(t *testing.T) {
	paths := securityPaths(t)
	writePolicyConfig(t, paths, "[policy]\nautonomy_profile = \"no-such-profile\"\n")
	res := runSecurityCheck(t, newPolicyDoctorCheck(paths))
	if res.Status != doctor.StatusWarn || !strings.Contains(res.Message, "[policy] section is invalid") || res.Remediation == "" {
		t.Fatalf("malformed [policy]: status=%s message=%q remedy=%q, want a warning with a remedy", res.Status, res.Message, res.Remediation)
	}
	writePolicyConfig(t, paths, "[policy\nautonomy_profile = \n")
	if res := runSecurityCheck(t, newPolicyDoctorCheck(paths)); res.Status != doctor.StatusWarn || !strings.Contains(res.Message, "could not be loaded") {
		t.Fatalf("unparseable config: status=%s message=%q, want a load warning", res.Status, res.Message)
	}
}

func TestDoctorPolicyHealthyIsOK(t *testing.T) {
	paths := securityPaths(t)
	writePolicyConfig(t, paths, "[policy]\nautonomy_profile = \"strict\"\n")
	drv, _ := seedAudit(t, paths, 1)
	deny, err := policy.NewStoreDenyList(drv)
	if err != nil {
		t.Fatalf("deny list: %v", err)
	}
	if err := deny.Add(context.Background(), "rm -rf *", policy.ActionClass(0)); err != nil {
		t.Fatalf("seed a deny rule: %v", err)
	}
	closeStoreClean(t, drv, paths)
	res := runSecurityCheck(t, newPolicyDoctorCheck(paths))
	if res.Status != doctor.StatusOK || !strings.Contains(res.Message, "strict") || !strings.Contains(res.Message, "1 deny rule(s)") {
		t.Fatalf("healthy policy: status=%s message=%q, want OK naming profile strict and 1 deny rule", res.Status, res.Message)
	}
	if bare := securityPaths(t); runSecurityCheck(t, newPolicyDoctorCheck(bare)).Status != doctor.StatusOK {
		t.Fatal("a clean machine with no config and no store must be OK")
	}
}

func TestDoctorApprovalKeyAbsentIsOK(t *testing.T) {
	paths := securityPaths(t)
	calls := useRecordedCustody(t)
	res := runSecurityCheck(t, newApprovalKeyDoctorCheck(paths))
	if res.Status != doctor.StatusOK || !strings.Contains(res.Message, "generated at first daemon run") {
		t.Fatalf("absent key: status=%s message=%q, want OK 'generated at first daemon run'", res.Status, res.Message)
	}
	if entries, _ := os.ReadDir(paths.DataDir()); len(entries) != 0 {
		t.Fatalf("the check created vault files: %v", entries)
	}
	if calls.String() != "" {
		t.Fatalf("keychain runner called: %q", calls.String())
	}
}

func TestDoctorApprovalKeyUnreadableWarns(t *testing.T) {
	paths := securityPaths(t)
	useRecordedCustody(t)
	if err := os.WriteFile(filepath.Join(paths.DataDir(), "vault.key"), []byte("short"), 0o600); err != nil {
		t.Fatalf("seed a corrupt vault key: %v", err)
	}
	if res := runSecurityCheck(t, newApprovalKeyDoctorCheck(paths)); res.Status != doctor.StatusWarn {
		t.Fatalf("unreadable vault: status=%s message=%q, want warn", res.Status, res.Message)
	}
}

func TestDoctorApprovalKeyStoredValueIsChecked(t *testing.T) {
	paths := securityPaths(t)
	useRecordedCustody(t)
	vault, err := secrets.SelectCustody(secrets.Config{Service: vaultService, Dir: paths.DataDir(), ForceFileVault: true,
		Runner: func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("no host keychain in this test")
		}})
	if err != nil {
		t.Fatalf("seed vault: %v", err)
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	for _, tc := range []struct {
		name  string
		value []byte
		want  doctor.Status
	}{
		{"consistent pair", priv, doctor.StatusOK},
		{"wrong length", []byte("not-a-key"), doctor.StatusWarn},
		{"mismatched halves", append(append([]byte(nil), priv[:ed25519.SeedSize]...), make([]byte, ed25519.PublicKeySize)...), doctor.StatusWarn},
	} {
		if err := vault.Set(context.Background(), secrets.ApprovalKeyName, tc.value); err != nil {
			t.Fatalf("%s: seed: %v", tc.name, err)
		}
		if res := runSecurityCheck(t, newApprovalKeyDoctorCheck(paths)); res.Status != tc.want {
			t.Errorf("%s: status=%s message=%q, want %s", tc.name, res.Status, res.Message, tc.want)
		}
	}
}

func TestDoctorEgressAndPrincipalChecksStateTheirContent(t *testing.T) {
	egressRes := runSecurityCheck(t, newEgressAuditOnlyDoctorCheck())
	if egressRes.Status != doctor.StatusOK || !strings.Contains(egressRes.Detail, "oauth: ") {
		t.Fatalf("egress_audit_only: status=%s detail=%q, want OK listing class: reason lines", egressRes.Status, egressRes.Detail)
	}
	principal := runSecurityCheck(t, newCallerPrincipalDoctorCheck())
	if principal.Status != doctor.StatusOK || !strings.Contains(principal.Message, "not a boundary against shell code running as the same user") {
		t.Fatalf("caller_principal: status=%s message=%q, want the ADR-0006 threat model", principal.Status, principal.Message)
	}
}

// TestDoctorSecurityChecksAreMountedInTheRealRegistry removes any of the six
// Register calls from productionCheckRegistry and fails.
func TestDoctorSecurityChecksAreMountedInTheRealRegistry(t *testing.T) {
	useTempCustody(t)
	useRecordedCustody(t)
	withElevationSource(t, elevation.CustodyNone)
	reg, err := productionCheckRegistry(context.Background(), securityPaths(t), doctorTestClock())
	if err != nil {
		t.Fatalf("productionCheckRegistry: %v", err)
	}
	mounted := map[string]bool{}
	for _, check := range reg.List() {
		mounted[check.Name()] = true
	}
	for _, want := range []string{"elevation_custody", "audit_chain", "policy", "approval_key", "egress_audit_only", "caller_principal"} {
		if !mounted[want] {
			t.Errorf("%s is not registered on the real productionCheckRegistry", want)
		}
	}
}

// TestDoctorApprovalKeyMakesNoKeychainWrite runs the check through the real
// read-only selector with the platform runner NOT bypassed, and requires that
// every runner call is a read: a writing selector would record an add or a
// delete (the probe item). Only darwin reaches the keychain through the runner.
func TestDoctorApprovalKeyMakesNoKeychainWrite(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("only darwin reaches the keychain through the injected runner; linux uses D-Bus")
	}
	paths := securityPaths(t)
	calls := &callLog{}
	previous := doctorApprovalKeyCustody
	doctorApprovalKeyCustody = func(cfg secrets.Config) (secrets.Custody, error) {
		cfg.ForceFileVault = false
		cfg.Runner = func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls.add(name + " " + strings.Join(args, " "))
			return nil, errors.New("no host keychain in this test")
		}
		return secrets.SelectCustodyReadOnly(cfg)
	}
	t.Cleanup(func() { doctorApprovalKeyCustody = previous })
	runSecurityCheck(t, newApprovalKeyDoctorCheck(paths))
	for _, call := range calls.calls {
		for _, write := range []string{"add-", "delete-", "update-", "import", "set-"} {
			if strings.Contains(call, write) {
				t.Fatalf("the approval_key check made a keychain write call: %q", call)
			}
		}
	}
}

// TestDoctorAuditChainDanglingDatabaseLinkIsError: a cascade.db symlink whose
// target is gone is not "no audit log yet".
func TestDoctorAuditChainDanglingDatabaseLinkIsError(t *testing.T) {
	paths := securityPaths(t)
	if err := os.Symlink(filepath.Join(paths.Root(), "missing-target.db"), auditDBPath(paths)); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	if res := runSecurityCheck(t, newAuditChainDoctorCheck(paths)); res.Status != doctor.StatusError {
		t.Fatalf("dangling database link: status=%s message=%q, want error", res.Status, res.Message)
	}
}

func sumOrAbsent(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "absent"
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// TestDoctorAuditChainWhileWriterHoldsOpenTransaction: a real second
// connection holds a write transaction open on the live store (WAL and shm
// present); the check still verifies and changes neither cascade.db nor -wal.
func TestDoctorAuditChainWhileWriterHoldsOpenTransaction(t *testing.T) {
	paths := securityPaths(t)
	drv, _ := seedAudit(t, paths, 3)
	defer func() { _ = drv.Close() }()
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- drv.Tx(context.Background(), func(c context.Context, tx provider.Tx) error {
			if err := tx.Put(c, "scratch", "k", []byte("pending")); err != nil {
				return err
			}
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	db := auditDBPath(paths)
	beforeDB, beforeWAL := sumOrAbsent(t, db), sumOrAbsent(t, db+"-wal")
	res := runSecurityCheck(t, newAuditChainDoctorCheck(paths))
	afterDB, afterWAL := sumOrAbsent(t, db), sumOrAbsent(t, db+"-wal")
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("writer transaction: %v", err)
	}
	if res.Status != doctor.StatusOK || !strings.Contains(res.Message, "verified") {
		t.Fatalf("live store: status=%s message=%q, want a verified chain", res.Status, res.Message)
	}
	if beforeDB != afterDB || beforeWAL != afterWAL || beforeWAL == "absent" {
		t.Fatalf("live store: the check changed cascade.db or -wal (wal before %s)", beforeWAL)
	}
}

// TestDoctorAuditChainHalfPresentWALWarns: exactly one sidecar is a writer
// starting, stopping or crashed, not tampering: a warning, never an error or
// an OK, and nothing is created.
func TestDoctorAuditChainHalfPresentWALWarns(t *testing.T) {
	paths := securityPaths(t)
	drv, _ := seedAudit(t, paths, 2)
	closeStoreClean(t, drv, paths)
	if err := os.WriteFile(auditDBPath(paths)+"-shm", []byte("leftover"), 0o600); err != nil {
		t.Fatalf("seed a lone shm: %v", err)
	}
	before := dataDigest(t, paths.Root())
	res := runSecurityCheck(t, newAuditChainDoctorCheck(paths))
	if res.Status != doctor.StatusWarn || !strings.Contains(res.Message, "not verified") {
		t.Fatalf("half-present WAL: status=%s message=%q, want a warning", res.Status, res.Message)
	}
	if dataDigest(t, paths.Root()) != before {
		t.Fatal("the refused open changed the data directory")
	}
}

// TestDoctorAuditChainStoreWithNoSchemaWarns: a cascade.db another check or a
// starting daemon has created but not yet given its kv table is a transition:
// a warning, never an error, and nothing is created.
func TestDoctorAuditChainStoreWithNoSchemaWarns(t *testing.T) {
	paths := securityPaths(t)
	if err := os.WriteFile(auditDBPath(paths), nil, 0o600); err != nil {
		t.Fatalf("seed an empty database: %v", err)
	}
	before := dataDigest(t, paths.Root())
	res := runSecurityCheck(t, newAuditChainDoctorCheck(paths))
	if res.Status != doctor.StatusWarn || !strings.Contains(res.Message, "no schema yet") {
		t.Fatalf("empty database: status=%s message=%q, want a warning", res.Status, res.Message)
	}
	if dataDigest(t, paths.Root()) != before {
		t.Fatal("the refused open changed the data directory")
	}
}
