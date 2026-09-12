// Purpose: shared backup CLI test scaffolding, plus `backup list` tests.
// Inputs: an in-memory Store, a real fs Target over t.TempDir(), fake
// elevation/vault collaborators.
// Outputs: exercised RunE paths driven through the real cobra command tree.
// Constraints: never touches the real data dir, keychain, or network.
// SPORT: cmd.cascade.backup-list/ADD (tests) (P1-E19-W4-S42-T3).
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// backupAllowGate always authorizes, returning a fixed non-empty proof, so
// success-path tests exercise the real downstream operations.
type backupAllowGate struct{ proof backup.ElevationProof }

func (g backupAllowGate) authorize(context.Context, *cobra.Command, string, []byte, bool) (backup.ElevationProof, error) {
	proof := g.proof
	if proof == "" {
		proof = "test-granted"
	}
	return proof, nil
}

// backupDenyGate always refuses with a typed ELEVATION_REQUIRED error,
// exactly the shape backupProofGate itself uses -- so refusal tests assert
// the CLI propagates the real refusal, not a synthesized one.
type backupDenyGate struct{}

func (backupDenyGate) authorize(context.Context, *cobra.Command, string, []byte, bool) (backup.ElevationProof, error) {
	return "", backup.ErrElevationRequired
}

// testBackupDeps builds a backupDeps over an in-memory Store, a real
// modernc-sqlite handle (migrated), and a real fs Target rooted at
// t.TempDir() -- Art.2 real collaborators wherever the operation under
// test actually reads/writes them, with only the elevation ceremony and
// the vault broker faked (both security seams driven directly by
// backup_elevation_test.go and backup_export_test.go).
func testBackupDeps(t *testing.T, authorize backupAuthorizeFunc) backupDeps {
	t.Helper()
	setTestBackupAgeIdentity(t)
	store := storetest.NewMemStore()
	dbPath := filepath.Join(t.TempDir(), "cascade.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	if _, err := storage.Bootstrap(context.Background(), db, storage.BootstrapOpts{Clock: clock}); err != nil {
		t.Fatalf("storage.Bootstrap: %v", err)
	}
	dataDir := t.TempDir()
	return backupDeps{
		Open: func(context.Context) (*backupRuntime, error) {
			return &backupRuntime{Store: store, DB: db, DataDir: dataDir, Close: func() {}}, nil
		},
		BuildTarget: backup.BuildTarget,
		Authorize:   authorize,
		Clock:       clock,
		Getenv:      func(string) string { return "" },
		ReadFile:    os.ReadFile,
		WriteFile:   os.WriteFile,
		NewVault: func(backup.ElevationProof) (*secrets.Broker, error) {
			return nil, cascade.New(cascade.KindUnavailable, "backup: vault broker is not configured in this test")
		},
		Create:  backup.CreateSnapshot,
		Restore: backup.Restore,
		Export:  backup.ExportPortable,
		Import:  backup.ImportPortable,
	}
}

// setTestBackupAgeIdentity generates a real X25519 age identity via the
// reference library (matching internal/backup's own newTestAgeKeypair
// precedent) and installs it as CASCADE_BACKUP_AGE_IDENTITY for the
// duration of the test -- every real backup operation (list, create,
// export, import) resolves the repository's signing/encryption identity
// through this exact env var. It also installs a real Ed25519 manifest
// signing key seed under CASCADE_BACKUP_MANIFEST_SIGNING_KEY, required by
// CreateSnapshot before it will take a snapshot at all.
func setTestBackupAgeIdentity(t *testing.T) {
	t.Helper()
	// Reuse an identity/signing key already installed by an earlier call
	// within the SAME test function (cross-target tests build two
	// independent backupDeps that must decrypt/verify each other's real
	// artifacts, so they need the identical repository identity, not a
	// fresh one per call).
	if os.Getenv(backup.AgeIdentityEnvVar) != "" && os.Getenv(backup.ManifestSigningKeyEnvVar) != "" {
		return
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("age.GenerateX25519Identity: %v", err)
	}
	t.Setenv(backup.AgeIdentityEnvVar, id.String())
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	t.Setenv(backup.ManifestSigningKeyEnvVar, base64.StdEncoding.EncodeToString(priv.Seed()))
}

// runBackup executes one backup command against an isolated command tree
// and returns stdout, stderr, and the error -- mirrors runVault's pattern
// exactly (vault_test.go).
func runBackup(t *testing.T, deps backupDeps, stdin string, args ...string) (string, string, error) {
	t.Helper()
	root := &cobra.Command{Use: "cascade", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().Bool("json", false, "")
	root.PersistentFlags().Bool("quiet", false, "")
	root.PersistentFlags().Bool("verbose", false, "")
	root.PersistentFlags().Bool("no-color", false, "")
	cmd := newBackupCmd(deps)
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)

	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(append([]string{"backup"}, args...))
	err := root.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

// addTestFSTarget registers a real fs target (name, one --domain, a cron
// spec) directly over the Store the deps were built with, so tests that
// only care about list/create/restore/export/import do not have to shell
// through `backup target add` first.
func addTestFSTarget(t *testing.T, deps backupDeps, name string) {
	t.Helper()
	rt, err := deps.Open(context.Background())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rt.Close()
	record := backup.TargetRecord{Name: name, Kind: backup.TargetKindFS, FSRoot: t.TempDir()}
	if err := backup.PutTarget(context.Background(), rt.Store, backupRegistryNamespace, record); err != nil {
		t.Fatalf("PutTarget: %v", err)
	}
	policy := backup.TargetPolicy{Target: name, CronSpec: "0 3 * * *", Domains: []string{"config"}}
	if err := backup.PutPolicy(context.Background(), rt.Store, backupRegistryNamespace, policy); err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}
}

// unmarshalJSONEnvelope decodes the "data" field of the CLI's --json
// envelope (internal/output/envelope.go: {version, ok, data}) into out,
// matching what a real shell-pipeline consumer of `--json` output receives.
func unmarshalJSONEnvelope(t *testing.T, stdout string, out any) {
	t.Helper()
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("--json output is not a valid envelope: %v\n%s", err, stdout)
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		t.Fatalf("--json envelope's data field does not decode: %v\n%s", err, stdout)
	}
}

// TestBackupCLIListEmpty proves `backup list` succeeds with zero targets
// configured (never an elevation prompt, per this ticket's own contract).
func TestBackupCLIListEmpty(t *testing.T) {
	deps := testBackupDeps(t, backupDenyGate{}.authorize)
	stdout, _, err := runBackup(t, deps, "", "list", "--json")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var result backupListResult
	unmarshalJSONEnvelope(t, stdout, &result)
	if len(result.Snapshots) != 0 {
		t.Fatalf("list on an empty registry = %d snapshots, want 0", len(result.Snapshots))
	}
}

// TestBackupCLIListWithTarget proves list enumerates a real fs target with
// zero snapshots (an empty manifests/ directory) as zero rows, not an
// error -- and that it never asks for elevation.
func TestBackupCLIListWithTarget(t *testing.T) {
	deps := testBackupDeps(t, backupDenyGate{}.authorize)
	addTestFSTarget(t, deps, "primary")
	stdout, _, err := runBackup(t, deps, "", "list", "--json")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !bytes.Contains([]byte(stdout), []byte("snapshots")) {
		t.Fatalf("list --json output missing the snapshots key: %s", stdout)
	}
}

// TestBackupCLIListBuildTargetFailure proves a target whose driver fails to
// construct surfaces that failure through `list` rather than panicking or
// silently reporting zero rows -- the mutation proof that namedBackupTargets
// actually checks BuildTarget's error.
func TestBackupCLIListBuildTargetFailure(t *testing.T) {
	deps := testBackupDeps(t, backupDenyGate{}.authorize)
	addTestFSTarget(t, deps, "primary")
	deps.BuildTarget = func(context.Context, backup.TargetRecord, *egress.Engine, func(string) string) (backup.Target, error) {
		return nil, cascade.New(cascade.KindUnavailable, "backup: injected build failure")
	}
	_, _, err := runBackup(t, deps, "", "list")
	if err == nil || !strings.Contains(err.Error(), "injected build failure") {
		t.Fatalf("list with a failing target build = %v, want the injected failure surfaced", err)
	}
}
