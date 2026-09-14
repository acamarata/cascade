package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// builtinManifest is a minimal, valid cascade.plugin/v2 manifest with an
// empty requires list and RuntimeBuiltin — the one shape AddPlugin can
// install without elevation.
const builtinManifest = `
id = "demo"
name = "Demo"
schema = "cascade.plugin/v2"
version = "1.0.0"
host_version = ">=2.0.0"
runtime = "builtin"
`

// processManifest mirrors builtinManifest but declares runtime = "process",
// which §5.14 always elevates regardless of grants.
const processManifest = `
id = "demo"
name = "Demo"
schema = "cascade.plugin/v2"
version = "1.0.0"
host_version = ">=2.0.0"
runtime = "process"
`

// grantingManifest declares one required capability, so installing it
// fresh (no prior install) is a grant-expansion over the empty set.
const grantingManifest = `
id = "demo"
name = "Demo"
schema = "cascade.plugin/v2"
version = "1.0.0"
host_version = ">=2.0.0"
runtime = "builtin"
requires = ["net.http"]
`

func TestPluginAdd_Idempotent(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()

	first, err := AddPlugin(ctx, store, []byte(builtinManifest), "", nil, true)
	if err != nil {
		t.Fatalf("first add: %v", err)
	}
	if first.Outcome != AddOutcomeInstalled {
		t.Fatalf("first add outcome = %v, want AddOutcomeInstalled", first.Outcome)
	}

	second, err := AddPlugin(ctx, store, []byte(builtinManifest), "", nil, true)
	if err != nil {
		t.Fatalf("second add (same version): %v", err)
	}
	if second.Outcome != AddOutcomeAlreadyInstalled {
		t.Fatalf("second add outcome = %v, want AddOutcomeAlreadyInstalled (§5.9)", second.Outcome)
	}
	if second.Metadata.InstalledVersion != "1.0.0" {
		t.Fatalf("second add metadata version = %q, want 1.0.0", second.Metadata.InstalledVersion)
	}

	msg := AlreadyInstalledMessage(second.Metadata.Name, second.Metadata.InstalledVersion)
	if msg != "already installed at v1.0.0 (demo)" {
		t.Fatalf("AlreadyInstalledMessage = %q", msg)
	}
}

func TestPluginAdd_ChecksumMismatch(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	artifact := []byte("real bundle bytes")
	wrongSum := hex.EncodeToString(sha256.New().Sum([]byte("not the artifact")))

	_, err := AddPlugin(ctx, store, []byte(builtinManifest), wrongSum, artifact, true)
	if err == nil {
		t.Fatal("AddPlugin with a mismatched checksum succeeded, want ErrChecksumMismatch")
	}
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindIntegrity {
		t.Fatalf("checksum mismatch error kind = %v (ok=%v), want KindIntegrity", kind, ok)
	}
	if _, ok, _ := LoadMetadata(ctx, store, "demo"); ok {
		t.Fatal("a checksum mismatch must never write a metadata record")
	}
}

func TestPluginAdd_ChecksumMatch(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	artifact := []byte("real bundle bytes")
	sum := sha256.Sum256(artifact)
	checksum := hex.EncodeToString(sum[:])

	res, err := AddPlugin(ctx, store, []byte(builtinManifest), checksum, artifact, true)
	if err != nil {
		t.Fatalf("AddPlugin with a matching checksum: %v", err)
	}
	if res.Metadata.PinnedChecksum != checksum {
		t.Fatalf("PinnedChecksum = %q, want %q", res.Metadata.PinnedChecksum, checksum)
	}
}

func TestPluginAdd_ProcessTierRequiresElevation(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()

	res, err := AddPlugin(ctx, store, []byte(processManifest), "", nil, true /* daemonAvailable */)
	if err != nil {
		t.Fatalf("process-tier add with a daemon available: %v", err)
	}
	if res.Outcome != AddOutcomeElevationRequired {
		t.Fatalf("process-tier add outcome = %v, want AddOutcomeElevationRequired", res.Outcome)
	}
	if _, ok, _ := LoadMetadata(ctx, store, "demo"); ok {
		t.Fatal("an elevation-required add must never write a metadata record itself")
	}
}

func TestPluginAdd_DaemonlessElevatedRefusal(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()

	// Process-tier: always elevated.
	if _, err := AddPlugin(ctx, store, []byte(processManifest), "", nil, false /* daemonAvailable */); err == nil {
		t.Fatal("process-tier add with no daemon succeeded, want the daemon-required refusal")
	} else if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Fatalf("process-tier daemonless error kind = %v (ok=%v), want KindUnavailable", kind, ok)
	}

	// Fresh grant-expansion on a builtin-tier plugin: also elevated.
	if _, err := AddPlugin(ctx, store, []byte(grantingManifest), "", nil, false); err == nil {
		t.Fatal("grant-expanding add with no daemon succeeded, want the daemon-required refusal")
	}

	// Never silently downgraded: no record exists after either refusal.
	if _, ok, _ := LoadMetadata(ctx, store, "demo"); ok {
		t.Fatal("a daemonless-refused add must never write a metadata record")
	}
}

func TestPluginAdd_MalformedManifestRefused(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if _, err := AddPlugin(ctx, store, []byte("not = valid = toml = garbage"), "", nil, true); err == nil {
		t.Fatal("AddPlugin with a malformed manifest succeeded, want a parse error")
	}
}
