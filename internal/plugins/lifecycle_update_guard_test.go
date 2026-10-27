// Purpose: the fail-closed guards on UpdatePlugin: an uninstalled id is never
// installed by an update, a process-tier update it cannot handshake is
// refused, and a remote-runtime candidate is refused. Every refusal leaves the
// stored record byte-identical (sha256 of the stored bytes compared).
// SPORT: internal/plugins lifecycle-update guards.
package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// updateRemoteManifest is a remote-runtime candidate for the seeded "demo".
const updateRemoteManifest = `
id = "demo"
name = "Demo"
schema = "cascade.plugin/v2"
version = "1.1.0"
host_version = ">=2.0.0"
runtime = "remote"

[remote]
host = "127.0.0.1"
port = 1
`

// okHandshaker confirms every handshake and counts the calls.
type okHandshaker struct{ calls *int }

func (h okHandshaker) Handshake(context.Context, string, string) error {
	*h.calls++
	return nil
}

// storedRecordHash is the sha256 of the raw bytes stored for name, or "absent".
func storedRecordHash(t *testing.T, store provider.Store, name string) string {
	t.Helper()
	raw, err := store.Get(context.Background(), HostNamespace, metadataKey(name))
	if err != nil {
		if kind, ok := cascade.KindOf(err); ok && kind == cascade.KindNotFound {
			return "absent"
		}
		t.Fatalf("read stored record %q: %v", name, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// requireRefusal fails unless err carries kind and every fragment.
func requireRefusal(t *testing.T, err error, kind cascade.Kind, fragments ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("got no error, want a %v refusal", kind)
	}
	if got, ok := cascade.KindOf(err); !ok || got != kind {
		t.Fatalf("error kind = %v (ok=%v), want %v: %v", got, ok, kind, err)
	}
	for _, f := range fragments {
		if !strings.Contains(err.Error(), f) {
			t.Fatalf("error %q does not name %q", err.Error(), f)
		}
	}
}

func TestUpdatePlugin_RefusesUninstalled(t *testing.T) {
	ctx := context.Background()
	manifestBytes := []byte(builtinManifest)
	sum := sha256.Sum256(manifestBytes)
	cases := []struct {
		name     string
		registry RegistryVersionChecker
		checksum string
	}{
		{"from", nil, ""},
		{"registry", okRegistry{version: "1.0.0"}, hex.EncodeToString(sum[:])},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := storetest.NewMemStore()
			_, err := UpdatePlugin(ctx, store, tc.registry, nil, manifestBytes, tc.checksum, manifestBytes, true)
			requireRefusal(t, err, cascade.KindNotFound, "demo is not installed", "cascade plugin add")
			if got := storedRecordHash(t, store, "demo"); got != "absent" {
				t.Fatalf("an update of an uninstalled plugin wrote a record (sha256 %s)", got)
			}
		})
	}
}

// TestUpdatePlugin_ProcessTierFreshManifestNeverCommits: a process-tier
// candidate for a name with no record, a handshake that would succeed and a
// daemon present still writes nothing.
func TestUpdatePlugin_ProcessTierFreshManifestNeverCommits(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	calls := 0
	_, err := UpdatePlugin(ctx, store, nil, okHandshaker{calls: &calls}, []byte(processManifest), "", nil, true)
	requireRefusal(t, err, cascade.KindNotFound, "cascade plugin add")
	if got := storedRecordHash(t, store, "demo"); got != "absent" {
		t.Fatalf("a fresh process-tier update committed a record (sha256 %s)", got)
	}
	if calls != 0 {
		t.Fatalf("the handshake ran %d times for an uninstalled plugin, want 0", calls)
	}
}

func TestUpdatePlugin_ProcessTierNilHandshakerRefuses(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if err := SaveMetadata(ctx, store, PluginMetadata{Name: "demo", InstalledVersion: "1.0.0", Enabled: true, RuntimeMode: plugin.RuntimeProcess}); err != nil {
		t.Fatal(err)
	}
	before := storedRecordHash(t, store, "demo")
	_, err := UpdatePlugin(ctx, store, nil, nil, []byte(updateProcessTierManifest), "", nil, true)
	requireRefusal(t, err, cascade.KindUnavailable, "cannot confirm the process handshake; update not applied")
	if after := storedRecordHash(t, store, "demo"); after != before {
		t.Fatalf("the prior record changed on a refused update: %s -> %s", before, after)
	}

	// The same candidate with a handshake that succeeds commits: the refusal
	// above is the missing handshaker, not the runtime itself.
	calls := 0
	res, err := UpdatePlugin(ctx, store, nil, okHandshaker{calls: &calls}, []byte(updateProcessTierManifest), "", nil, true)
	if err != nil || res.Outcome != UpdateOutcomeUpdated || calls != 1 {
		t.Fatalf("update with a confirming handshake = %+v, %v (calls %d), want updated after one handshake", res.Outcome, err, calls)
	}
}

func TestUpdatePlugin_ToRemoteRuntimeRefused(t *testing.T) {
	ctx := context.Background()
	for _, daemon := range []bool{false, true} {
		store := storetest.NewMemStore()
		seedInstalled(t, store, "1.0.0", nil)
		before := storedRecordHash(t, store, "demo")
		_, err := UpdatePlugin(ctx, store, nil, nil, []byte(updateRemoteManifest), "", nil, daemon)
		requireRefusal(t, err, cascade.KindUnsupported, "DEF-P2-remote-plugin-runtime")
		if after := storedRecordHash(t, store, "demo"); after != before {
			t.Fatalf("daemon=%v: the prior record changed on a remote refusal", daemon)
		}
	}
	t.Run("elevated", assertElevatedRemoteUpdateRefused)
}
