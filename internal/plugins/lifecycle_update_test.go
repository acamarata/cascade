package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// updatedManifest is builtinManifest (lifecycle_add_test.go) bumped to a
// new version, no new requires — the plain, non-elevated update path.
const updatedManifest = `
id = "demo"
name = "Demo"
schema = "cascade.plugin/v2"
version = "1.1.0"
host_version = ">=2.0.0"
runtime = "builtin"
`

// expandedManifest bumps the version AND adds a capability not in
// builtinManifest's (empty) requires — the grant-expansion path.
const expandedManifest = `
id = "demo"
name = "Demo"
schema = "cascade.plugin/v2"
version = "1.1.0"
host_version = ">=2.0.0"
runtime = "builtin"
requires = ["net.http"]
`

// alwaysFailHandshaker always reports a handshake failure, driving
// UpdatePlugin's rollback path.
type alwaysFailHandshaker struct{ err error }

func (h alwaysFailHandshaker) Handshake(context.Context, string, string) error { return h.err }

// alwaysErrRegistry always fails LatestVersion, exercising §5.19's
// allowed-fail leg from the "checker present but unreachable" side (as
// opposed to nil, which lifecycle_update_test.go's other case covers).
type alwaysErrRegistry struct{}

func (alwaysErrRegistry) LatestVersion(context.Context, string) (string, error) {
	return "", errors.New("registry: connection refused")
}

func seedInstalled(t *testing.T, store *storetest.MemStore, version string, grants []string) {
	t.Helper()
	if err := SaveMetadata(context.Background(), store, PluginMetadata{
		Name: "demo", InstalledVersion: version, Enabled: true, Grants: grants,
	}); err != nil {
		t.Fatalf("seedInstalled: %v", err)
	}
}

func TestPluginUpdate_RegistryUnavailGraceful(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name     string
		registry RegistryVersionChecker
	}{
		{"nil registry", nil},
		{"erroring registry", alwaysErrRegistry{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := storetest.NewMemStore()
			seedInstalled(t, store, "1.0.0", nil)

			res, err := UpdatePlugin(ctx, store, tc.registry, nil, []byte(updatedManifest), "", nil, true)
			if err != nil {
				t.Fatalf("UpdatePlugin: %v (§5.19 registry unavailability must never be an error)", err)
			}
			if res.RegistryNotice != registryUnavailableNotice {
				t.Fatalf("RegistryNotice = %q, want the graceful notice", res.RegistryNotice)
			}
			if res.Outcome != UpdateOutcomeUpdated {
				t.Fatalf("Outcome = %v, want UpdateOutcomeUpdated (registry unavailability never blocks the update)", res.Outcome)
			}
		})
	}
}

func TestPluginUpdate_ChecksumMismatch(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	seedInstalled(t, store, "1.0.0", nil)
	wrongSum := hex.EncodeToString(sha256.New().Sum([]byte("wrong")))

	_, err := UpdatePlugin(ctx, store, nil, nil, []byte(updatedManifest), wrongSum, []byte("artifact"), true)
	if err == nil {
		t.Fatal("UpdatePlugin with a mismatched checksum succeeded, want ErrChecksumMismatch")
	}
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindIntegrity {
		t.Fatalf("checksum mismatch kind = %v (ok=%v), want KindIntegrity", kind, ok)
	}
	// The prior record must be untouched.
	rec, ok2, _ := LoadMetadata(ctx, store, "demo")
	if !ok2 || rec.InstalledVersion != "1.0.0" {
		t.Fatalf("prior record changed after a checksum-mismatch update: %+v (ok=%v)", rec, ok2)
	}
}

func TestPluginUpdate_GrantExpansionRequiresElevation(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	seedInstalled(t, store, "1.0.0", nil)

	res, err := UpdatePlugin(ctx, store, nil, alwaysFailHandshaker{}, []byte(expandedManifest), "", nil, true /* daemonAvailable */)
	if err != nil {
		t.Fatalf("grant-expanding update with a daemon available: %v", err)
	}
	if res.Outcome != UpdateOutcomeElevationRequired {
		t.Fatalf("Outcome = %v, want UpdateOutcomeElevationRequired", res.Outcome)
	}
	rec, _, _ := LoadMetadata(ctx, store, "demo")
	if rec.InstalledVersion != "1.0.0" {
		t.Fatalf("prior record changed while elevation was pending: %+v", rec)
	}

	if _, err := UpdatePlugin(ctx, store, nil, nil, []byte(expandedManifest), "", nil, false /* daemonAvailable */); err == nil {
		t.Fatal("grant-expanding update with no daemon succeeded, want the daemon-required refusal")
	} else if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Fatalf("daemonless update error kind = %v (ok=%v), want KindUnavailable", kind, ok)
	}
}

func TestPluginUpdate_Rollback(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	seedInstalled(t, store, "1.0.0", nil)

	failure := errors.New("plugin: handshake: hello_ack timed out")
	res, err := UpdatePlugin(ctx, store, nil, alwaysFailHandshaker{err: failure}, []byte(updatedManifest), "", nil, true)
	if err != nil {
		t.Fatalf("UpdatePlugin with a failing handshake: %v", err)
	}
	if res.Outcome != UpdateOutcomeRolledBack {
		t.Fatalf("Outcome = %v, want UpdateOutcomeRolledBack", res.Outcome)
	}
	if !errors.Is(res.RollbackErr, failure) {
		t.Fatalf("RollbackErr = %v, want it to wrap the handshake failure", res.RollbackErr)
	}
	if res.Metadata.InstalledVersion != "1.0.0" {
		t.Fatalf("RolledBack result reports version %q, want the restored prior version 1.0.0", res.Metadata.InstalledVersion)
	}

	// The store itself was never touched: the candidate version never
	// became the stored record (this IS the rollback — see UpdatePlugin's
	// doc comment).
	stored, ok, err := LoadMetadata(ctx, store, "demo")
	if err != nil || !ok || stored.InstalledVersion != "1.0.0" {
		t.Fatalf("stored record after rollback = %+v (ok=%v err=%v), want unchanged v1.0.0", stored, ok, err)
	}
}

func TestPluginUpdate_HandshakeSuccessCommits(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	seedInstalled(t, store, "1.0.0", nil)

	res, err := UpdatePlugin(ctx, store, nil, alwaysFailHandshaker{err: nil}, []byte(updatedManifest), "", nil, true)
	if err != nil {
		t.Fatalf("UpdatePlugin: %v", err)
	}
	if res.Outcome != UpdateOutcomeUpdated {
		t.Fatalf("Outcome = %v, want UpdateOutcomeUpdated", res.Outcome)
	}
	stored, ok, err := LoadMetadata(ctx, store, "demo")
	if err != nil || !ok || stored.InstalledVersion != "1.1.0" {
		t.Fatalf("stored record after a successful update = %+v (ok=%v err=%v), want v1.1.0", stored, ok, err)
	}
}
