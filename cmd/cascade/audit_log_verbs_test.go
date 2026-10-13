// Purpose: the audit-open refusals of the CLI verbs: backup create, backup
//
//	key export and plugin remove refuse with KindUnavailable when the
//	process audit log cannot be opened, and change nothing first.
//
// SPORT: cmd/cascade/audit_log (ADD, tests, P1-SEC-06).
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// failCustody makes every custody selection fail the way a host with no
// usable keychain does, and restores the selector when the test ends.
func failCustody(t *testing.T) {
	t.Helper()
	resetProcessFirewall(t)
	selectProcessCustody = func(secrets.Config) (secrets.Custody, error) { return nil, secrets.ErrNoCustodyAvailable() }
}

func TestBackupRefusesWithoutAuditLog(t *testing.T) {
	deps := testKeyVaultDeps(t, backupAllowGate{}.authorize)
	addTestFSTarget(t, deps, "primary")
	deps.AuditLog = productionBackupDeps().AuditLog
	deps.Getenv = func(k string) string {
		if k == "TEST_PASS" {
			return "fake-passphrase-1"
		}
		return ""
	}
	failCustody(t)

	if _, _, err := runBackup(t, deps, "", "create", "--target", "primary", "--yes"); !isCLIKind(err, cascade.KindUnavailable) {
		t.Fatalf("backup create without an audit log = %v, want KindUnavailable", err)
	}
	out := filepath.Join(t.TempDir(), "recovery.age")
	_, _, err := runBackup(t, deps, "", "key", "export", "--out", out, "--passphrase-env", "TEST_PASS", "--yes")
	if !isCLIKind(err, cascade.KindUnavailable) {
		t.Fatalf("backup key export without an audit log = %v, want KindUnavailable", err)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Fatal("a refused key export still wrote the recovery artifact")
	}
	stdout, _, err := runBackup(t, deps, "", "list", "--json")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var result backupListResult
	unmarshalJSONEnvelope(t, stdout, &result)
	if len(result.Snapshots) != 0 {
		t.Fatalf("a refused create still produced %d snapshot(s)", len(result.Snapshots))
	}
}

func TestPluginRemoveRefusesWithoutAuditLog(t *testing.T) {
	deps := testPluginDeps(t)
	manifestPath := filepath.Join(t.TempDir(), "demo.toml")
	if err := os.WriteFile(manifestPath, []byte(builtinManifestFixture), 0o600); err != nil {
		t.Fatalf("write manifest fixture: %v", err)
	}
	if _, _, err := runPluginCLI(t, deps, "add", manifestPath); err != nil {
		t.Fatalf("plugin add: %v", err)
	}
	failCustody(t)

	if _, _, err := runPluginCLI(t, deps, "remove", "demo"); !isCLIKind(err, cascade.KindUnavailable) {
		t.Fatalf("plugin remove without an audit log = %v, want KindUnavailable", err)
	}
	if _, _, err := runPluginCLI(t, deps, "info", "demo"); err != nil {
		t.Fatalf("the refused remove still deleted the plugin record: %v", err)
	}
}

// A composition with no store still gets a Log (it refuses every append),
// and two such opens are two Logs: nothing is memoized under a nil key.
func TestOpenAuditLogNilStoreIsNotShared(t *testing.T) {
	paths := auditTestPaths(t)
	clock := testkit.NewFrozenClock(auditTestEpoch)
	a, err := openAuditLog(nil, clock, nil, paths)
	if err != nil || a == nil {
		t.Fatalf("openAuditLog(nil store) = (%v, %v)", a, err)
	}
	b, _ := openAuditLog(nil, clock, nil, paths)
	if a == b {
		t.Fatal("two opens over a nil store returned one shared log")
	}
	if _, err := a.Append(context.Background(), redactionEvent("x", map[string]string{"n": "1"})); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Append over a nil store = %v, want KindUnavailable", err)
	}
}
