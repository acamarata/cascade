// Purpose: pins doctor_mounts_sources.go's nodesRecordStoreFor, the helper
// moved out of doctor_mounts.go: it answers nil (never an invented empty
// store) when no data directory resolves, and otherwise reads and writes the
// same file-backed device records `node serve` uses, under that directory.
// SPORT: DOCTOR_SECRETS_REGISTRATION: CHANGE (tests).
package main

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/nodes"
)

// TestNodesRecordStoreForEmptyDataDirIsNil: an unresolvable data directory
// must yield a nil store, which nodes.HealthCheck reports as an error. An
// empty store here would read as "no nodes enrolled".
func TestNodesRecordStoreForEmptyDataDirIsNil(t *testing.T) {
	if got := nodesRecordStoreFor(emptyDataDirPaths{}, doctorTestClock()); got != nil {
		t.Fatalf("nodesRecordStoreFor with no data directory = %v, want nil", got)
	}
}

// TestNodesRecordStoreForUsesDataDir enrolls through the returned store and
// asserts the record landed in <data dir>/nodes/devices.json, then reads it
// back through an independent backend over the same directory.
func TestNodesRecordStoreForUsesDataDir(t *testing.T) {
	paths := doctorTestPaths(t)
	store := nodesRecordStoreFor(paths, doctorTestClock())
	if store == nil {
		t.Fatal("nodesRecordStoreFor returned nil for a resolvable data directory")
	}
	pub := make(ed25519.PublicKey, ed25519.PublicKeySize)
	pub[0] = 7
	identity := nodes.Identity{NodeID: nodes.DeriveNodeID(pub), PubKey: pub}
	if _, err := store.Enroll(identity, nodes.TierWorkerTrusted); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.DataDir(), "nodes", "devices.json")); err != nil {
		t.Fatalf("the record file is not under the injected data directory: %v", err)
	}
	reread := nodes.NewRecordStore(nodes.NewFileRecordBackend(paths.DataDir()), doctorTestClock())
	if _, err := reread.Get(identity.NodeID); err != nil {
		t.Fatalf("an independent store over the same data dir cannot read the enrolled node: %v", err)
	}
}
