// Purpose: unit tests for node.go/node_admit.go/node_keys.go's cobra
//
//	wiring, over injected nodeCLIDeps so no test resolves the real
//	CASCADE_HOME or touches a real OS keychain (Art.7.1), mirroring
//	node_serve_test.go's established pattern.
//
// SPORT: cmd/cascade/node (ADD tests, P1-E17-W4-S36-T4).
package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeAvailableKeystore/fakeEnrolledBackend satisfy the two daemonless
// elevation preconditions unconditionally, so a test can drive an
// elevated verb PAST its Authorize gate and exercise the real logic that
// follows, without touching a real OS keychain (Art.7.1).
type fakeAvailableKeystore struct{}

func (fakeAvailableKeystore) GenerateKey() error          { return nil }
func (fakeAvailableKeystore) PubKeyB64() (string, error)  { return "", nil }
func (fakeAvailableKeystore) Sign([]byte) ([]byte, error) { return nil, nil }
func (fakeAvailableKeystore) IsAvailable() bool           { return true }
func (fakeAvailableKeystore) Tier() elevation.StorageTier { return elevation.TierOSKeychain }

type fakeEnrolledBackend struct{}

func (fakeEnrolledBackend) Load() (elevation.TrustRecord, bool, error) {
	return elevation.TrustRecord{TOFUAcknowledged: true}, true, nil
}
func (fakeEnrolledBackend) Save(elevation.TrustRecord) error { return nil }

// allowingGate returns a *elevationGate whose preconditions are always
// satisfied (no CASCADE_NO_INPUT set), for tests exercising the logic
// past an elevated verb's Authorize call.
func allowingGate(env map[string]string) *elevationGate {
	getenv := func(k string) string { return env[k] }
	return newElevationGate(
		func() elevation.ElevationKeystore { return fakeAvailableKeystore{} },
		func() elevation.Backend { return fakeEnrolledBackend{} },
		runtime.NewFixedClock(time.Now()), getenv,
	)
}

// fakeCmdWithContext returns a bare *cobra.Command carrying a background
// context and empty stdin, for tests that call runNodeAdmit directly
// rather than through cmd.Execute()/SetArgs.
func fakeCmdWithContext(t *testing.T) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetIn(strings.NewReader(""))
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("quiet", false, "")
	cmd.Flags().Bool("verbose", false, "")
	cmd.Flags().Bool("no-color", false, "")
	return cmd
}

func testNodeCLIDeps(t *testing.T, env map[string]string) nodeCLIDeps {
	t.Helper()
	root := shortTempDir(t)
	getenv := func(k string) string { return env[k] }
	return nodeCLIDeps{
		Paths:      fakeDaemonPaths{root: root},
		Clock:      runtime.NewFixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		SecretsDir: shortTempDir(t),
		GOOS:       "darwin",
		Getenv:     getenv,
		Gate:       newElevationGate(nil, nil, runtime.NewFixedClock(time.Now()), getenv),
	}
}

// seedRecord enrolls one device record directly against deps' own record
// store, for list/status/drain tests that need a record already present.
func seedRecord(t *testing.T, deps nodeCLIDeps, tier nodes.Tier) nodes.Identity {
	t.Helper()
	store, err := openRecordStore(deps)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := nodes.GenerateIdentity(deterministicReader{seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enroll(id, tier); err != nil {
		t.Fatal(err)
	}
	return id
}

// deterministicReader is a fixed-byte io.Reader for deterministic test key
// generation (Art.7.1 — never crypto/rand in a test needing a stable id).
type deterministicReader struct{ seed byte }

func (r deterministicReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.seed
	}
	return len(p), nil
}

func TestNodeListAndStatus(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	id := seedRecord(t, deps, nodes.TierWorkerTrusted)

	store, err := openRecordStore(deps)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := store.List()
	if err != nil || len(recs) != 1 {
		t.Fatalf("List() = %v, %v", recs, err)
	}
	rec, err := store.Get(id.NodeID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	view := newNodeRowView(rec, deps.Clock)
	if view.NodeID != id.NodeID || view.Tier != string(nodes.TierWorkerTrusted) {
		t.Fatalf("unexpected view: %+v", view)
	}
}

func TestNodeDrainCmd_RealTransition(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	id := seedRecord(t, deps, nodes.TierWorkerTrusted)

	cmd := newNodeDrainCmd(deps)
	cmd.SetArgs([]string{id.NodeID})
	cmd.SetContext(context.Background())
	if err := cmd.Execute(); err != nil {
		t.Fatalf("drain: %v", err)
	}

	store, err := openRecordStore(deps)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := store.Get(id.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Drained {
		t.Fatal("drain did not persist through the CLI entry point")
	}
}

func TestNodeDrainCmd_RefusedOnWindows(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	deps.GOOS = "windows"
	id := seedRecord(t, deps, nodes.TierWorkerTrusted)

	cmd := newNodeDrainCmd(deps)
	cmd.SetArgs([]string{id.NodeID})
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected a tier-2 refusal on windows")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Fatalf("got kind %v (ok=%v), want KindUnsupported", kind, ok)
	}
}

// TestNodeRemoveRequiresElevation proves the elevated verb refuses under
// CASCADE_NO_INPUT=1 and removes nothing.
func TestNodeRemoveRequiresElevation(t *testing.T) {
	deps := testNodeCLIDeps(t, map[string]string{"CASCADE_NO_INPUT": "1"})
	id := seedRecord(t, deps, nodes.TierWorkerTrusted)

	cmd := newNodeRemoveCmd(deps)
	cmd.SetArgs([]string{id.NodeID})
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindElevationRequired {
		t.Fatalf("got %v (kind ok=%v), want ELEVATION_REQUIRED", err, ok)
	}

	store, serr := openRecordStore(deps)
	if serr != nil {
		t.Fatal(serr)
	}
	if _, gerr := store.Get(id.NodeID); gerr != nil {
		t.Fatalf("refused remove deleted the record: %v", gerr)
	}
}

// TestNodeRotateKeyElevation is one of this ticket's named acceptance
// tests: rotate-key refuses under CASCADE_NO_INPUT=1, never a hang.
func TestNodeRotateKeyElevation(t *testing.T) {
	deps := testNodeCLIDeps(t, map[string]string{"CASCADE_NO_INPUT": "1"})
	id := seedRecord(t, deps, nodes.TierWorkerTrusted)

	cmd := newNodeRotateKeyCmd(deps)
	cmd.SetArgs([]string{id.NodeID})
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindElevationRequired {
		t.Fatalf("got %v (kind ok=%v), want ELEVATION_REQUIRED", err, ok)
	}
}

// TestNodeRevokeElevation is one of this ticket's named acceptance tests:
// revoke refuses under CASCADE_NO_INPUT=1, never a hang.
func TestNodeRevokeElevation(t *testing.T) {
	deps := testNodeCLIDeps(t, map[string]string{"CASCADE_NO_INPUT": "1"})
	id := seedRecord(t, deps, nodes.TierWorkerTrusted)

	cmd := newNodeRevokeCmd(deps)
	cmd.SetArgs([]string{id.NodeID})
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindElevationRequired {
		t.Fatalf("got %v (kind ok=%v), want ELEVATION_REQUIRED", err, ok)
	}

	store, serr := openRecordStore(deps)
	if serr != nil {
		t.Fatal(serr)
	}
	rec, gerr := store.Get(id.NodeID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if len(rec.RevokedKeys) != 0 {
		t.Fatal("refused revoke recorded a revoked key")
	}
}

// TestNodeEnrollRequiresTrustTier is one of this ticket's named acceptance
// tests (R-21.220): enroll without --trust-tier is a typed error, never a
// default, and writes no device record. Checked before elevation and
// before any ssh dial, so no Dialer is needed for this case.
func TestNodeEnrollRequiresTrustTier(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	err := runNodeAdmit(fakeCmdWithContext(t), deps, "worker@host1", "", "", "")
	if err == nil {
		t.Fatal("expected a typed error for a missing --trust-tier")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("got kind %v (ok=%v), want KindInvalidInput", kind, ok)
	}
}

// TestNodeEnrollUnknownHostKeyRefused proves the real ssh-dial host-key
// check refuses an unpinned host, end to end from the CLI entry point,
// via an injected fake Dialer (Art.7.2: the default unit lane forbids
// "net", so no real socket is opened).
func TestNodeEnrollUnknownHostKeyRefused(t *testing.T) {
	deps := testNodeCLIDeps(t, nil)
	deps.Gate = allowingGate(nil)
	deps.Dialer = fakeVerifyOnlyDialer{fingerprint: "deadbeef"}

	err := runNodeAdmit(fakeCmdWithContext(t), deps, "worker@host1", "worker-trusted", "", "")
	if err == nil {
		t.Fatal("expected an unknown-host-key refusal")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPermissionDenied {
		t.Fatalf("got kind %v (ok=%v), want KindPermissionDenied", kind, ok)
	}

	store, serr := openRecordStore(deps)
	if serr != nil {
		t.Fatal(serr)
	}
	recs, lerr := store.List()
	if lerr != nil || len(recs) != 0 {
		t.Fatalf("a refused host-key check must not enroll anything: %v records, err=%v", len(recs), lerr)
	}
}
