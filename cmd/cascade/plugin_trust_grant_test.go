//go:build !windows

// Purpose: the process-plugin trust grant over the daemon registry. plugin.add
// of the shipped cascade-github manifest, dispatched through
// newElevatedTestRegistry (the real elevation middleware over an injected
// platform-tier test custody, the plugin-install middleware and the
// production plugin.add handler), records TrustTier trusted, the injected
// executable's sha256 and the manifest's grant set; the same params unsigned
// are challenged and write nothing.
// Inputs: the elevation fixtures in daemon_unix_elevation_fixture_test.go; a
// fake executable under t.TempDir().
// SPORT: cmd/cascade plugin trust grant tests (P1-PLG-10).
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// fakeGithubExecutable injects a dir holding a fake, never-run cascade-github
// executable and returns its sha256 hex.
func fakeGithubExecutable(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	body := []byte("#!/bin/sh\n# fake cascade-github for tests; never executed\nexit 0\n")
	exe := filepath.Join(dir, "cascade-github")
	if err := os.WriteFile(exe, body, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(exe, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(plugins.SetExecutableDirForTest(dir))
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func TestProvisionElevatedTrustsOnlyWithWitness(t *testing.T) {
	ctx := context.Background()
	manifest, err := os.ReadFile(filepath.Join("..", "..", "plugins", "github", "manifest.toml"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := plugin.ParseManifest(bytes.NewReader(manifest))
	if err != nil || m.Runtime != plugin.RuntimeProcess {
		t.Fatalf("shipped manifest = %v, %v; want a process-tier manifest", m.Runtime, err)
	}
	digest := fakeGithubExecutable(t)
	var store provider.Store
	addTestRegistration(t, daemonRegistration{Name: "test-capture-trust-store", Phase: phaseLate, Order: 9002,
		Wire: func(w *daemonWiring) error { store = w.Store; return nil }})
	reg, call := newElevatedTestRegistry(t, filepath.Join(t.TempDir(), "data"))
	if store == nil {
		t.Fatal("the capture registration did not run")
	}
	sum := sha256.Sum256(manifest)
	params := pluginAddParams{ID: "cascade-github", Checksum: hex.EncodeToString(sum[:]), ManifestBytes: manifest}

	raw, _ := json.Marshal(params)
	owner := principal.WithCaller(ctx, principal.Caller{OwnerCLI: true})
	if _, eo := reg.Dispatch(owner, &rpc.Request{JSONRPC: "2.0", Method: "plugin.add", Params: raw}); eo == nil || eo.Code != cascade.RPCCodeElevationRequired {
		t.Fatalf("unsigned plugin.add = %+v, want ELEVATION_REQUIRED", eo)
	}
	if _, ok, _ := plugins.LoadMetadata(ctx, store, "cascade-github"); ok {
		t.Fatal("an unsigned plugin.add wrote a record")
	}

	if err := call(ctx, "plugin.add", params, nil); err != nil {
		t.Fatalf("signed plugin.add: %v", err)
	}
	rec, ok, err := plugins.LoadMetadata(ctx, store, "cascade-github")
	if err != nil || !ok {
		t.Fatalf("no record after a signed plugin.add: ok=%v err=%v", ok, err)
	}
	want := strings.Join(m.Requires, ",")
	if rec.TrustTier != plugins.TrustTrusted || rec.ExecDigest != digest || strings.Join(rec.TrustGrants, ",") != want {
		t.Fatalf("record = %+v, want trusted with digest %s and trust grants %s", rec, digest, want)
	}
	if got := strings.Join(plugins.EffectiveGrants(rec), ","); got != want {
		t.Fatalf("EffectiveGrants = %s, want %s", got, want)
	}
}
