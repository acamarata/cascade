// Purpose: unit tests for ci_attest_role.go's role-based trust store selection.
package main

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestAttestTrustForRoleRefusesBadRole: a binding file holding "{" gives an
// error and a nil store; on the controller role a key vouched only in
// ci_attest_trust is KindNotFound; on the node role it resolves.
func TestAttestTrustForRoleRefusesBadRole(t *testing.T) {
	dataDir := t.TempDir()
	clock := testkit.NewFrozenClock(testkit.NewRealClock().Now())
	assertMalformedBindingRefused(t, dataDir, clock)
	writeTestDesignation(t, dataDir) // the controller role below

	db := openAttestRoleDB(t, filepath.Join(dataDir, "cascade.db"), clock)
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	keyID := nodes.KeyIDFor(pub)
	nodeID := "test-node-id"

	// Controller role: a key absent from the record store is NotFound.
	controllerStore, err := attestTrustForRole(db, dataDir, clock)
	if err != nil {
		t.Fatalf("attestTrustForRole for controller: %v", err)
	}
	_, err = controllerStore.Lookup(context.Background(), nodeID, keyID)
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Fatalf("controller role lookup: got kind %v (ok=%v), want KindNotFound", kind, ok)
	}

	// Node role: with a binding and a vouched key, the key resolves.
	writeTestControllerBinding(t, dataDir, pub)
	vouchTestKey(t, db, nodeID, pub, clock.Now())
	nodeStore, err := attestTrustForRole(db, dataDir, clock)
	if err != nil {
		t.Fatalf("attestTrustForRole for node: %v", err)
	}
	resolvedKey, err := nodeStore.Lookup(context.Background(), nodeID, keyID)
	if err != nil {
		t.Fatalf("node role lookup failed: %v", err)
	}
	if !resolvedKey.Equal(pub) {
		t.Fatal("resolved key does not match vouched key")
	}
}

// assertMalformedBindingRefused writes a binding file holding "{", checks
// attestTrustForRole refuses it with a nil store, then removes the file.
func assertMalformedBindingRefused(t *testing.T, dataDir string, clock *testkit.FrozenClock) {
	t.Helper()
	path := filepath.Join(dataDir, "nodes", "controller_binding.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := attestTrustForRole(nil, dataDir, clock)
	if err == nil {
		t.Fatal("expected error for malformed binding file")
	}
	if store != nil {
		t.Fatal("expected nil store for malformed binding file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

// openAttestRoleDB opens a one-connection SQLite store with the jobs schema,
// closed when the test ends.
func openAttestRoleDB(t *testing.T, path string, clock *testkit.FrozenClock) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := jobs.ApplyJobsSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatal(err)
	}
	return db
}

// writeTestControllerBinding removes the designation and saves a controller
// binding for pub, which makes this home the node role.
func writeTestControllerBinding(t *testing.T, dataDir string, pub ed25519.PublicKey) {
	t.Helper()
	removeTestDesignation(t, dataDir)
	binding := nodes.ControllerBinding{
		Endpoint:            "https://example.com",
		EnrollmentID:        "test-enrollment",
		ControllerPubKeyB64: base64.StdEncoding.EncodeToString(pub),
	}
	bindingJSON, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "nodes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "nodes", "controller_binding.json"), bindingJSON, 0o600); err != nil {
		t.Fatal(err)
	}
}

// vouchTestKey vouches pub for nodeID in the node's ci_attest_trust store.
func vouchTestKey(t *testing.T, db *sql.DB, nodeID string, pub ed25519.PublicKey, at time.Time) {
	t.Helper()
	ciStore, err := jobs.NewCiAttestTrustStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := ciStore.Vouch(context.Background(), nodeID, nodes.KeyIDFor(pub), base64.StdEncoding.EncodeToString(pub), at); err != nil {
		t.Fatal(err)
	}
}

// TestCiAttestVerifyUsesVouchedStoreOnNode: fixture with an accepted, stored
// row and a saved controller binding (node role): verify --require fails
// although the record store holds the node; after Vouch(node, KeyIDFor(pub),
// pub) it passes. Without a binding (controller) the same row passes through
// the record store.
func TestCiAttestVerifyUsesVouchedStoreOnNode(t *testing.T) {
	f := newAttestFixture(t)
	db := f.openDB(t)
	defer func() { _ = db.Close() }()
	nonce := f.accept(t, db, "repo-a", "ckpt-1")
	f.store(t, db, "repo-a", nonce, "ckpt-1", 0)

	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	writeTestControllerBinding(t, f.dataDir, pub)
	err = f.runInProcess(t, f.commit, "--require", "--repo-id", "repo-a")
	if cascade.ExitCode(err) != 1 {
		t.Fatalf("verify without vouch: exit %d, want 1", cascade.ExitCode(err))
	}

	vouchTestKey(t, db, f.node.NodeID, f.node.PubKey, f.start)
	err = f.runInProcess(t, f.commit, "--require", "--repo-id", "repo-a")
	if cascade.ExitCode(err) != 0 {
		t.Fatalf("verify with vouch: %v (exit %d), want exit 0", err, cascade.ExitCode(err))
	}

	// Controller role (designation, no binding) reads the record store and passes.
	if err := os.Remove(filepath.Join(f.dataDir, "nodes", "controller_binding.json")); err != nil {
		t.Fatal(err)
	}
	writeTestDesignation(t, f.dataDir)
	err = f.runInProcess(t, f.commit, "--require", "--repo-id", "repo-a")
	if cascade.ExitCode(err) != 0 {
		t.Fatalf("verify as controller: %v (exit %d), want exit 0", err, cascade.ExitCode(err))
	}
}
