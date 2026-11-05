// Purpose: InstallTrusted's record is trusted only because a witness was
// dispatched: the same params through the same InstallMiddleware and
// ProvisionElevated chain, without the elevation middleware, write nothing.
// SPORT: internal/plugins/plugintest tests (P1-PLG-06).
package plugintest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

const demoManifest = `
id = "trustdemo"
name = "Trust Demo"
schema = "cascade.plugin/v2"
version = "1.0.0"
host_version = ">=2.0.0"
runtime = "process"
requires = ["net.http:api.github.com", "host.secret_ref"]
`

// fakeBody is a never-run executable body.
const fakeBody = "#!/bin/sh\nexit 0\n"

func TestPlugintestInstallTrustedUsesGrantPath(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("Windows tier-2 refuses elevation before the attestation flow")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, ExecutableName("trustdemo"))
	if err := os.WriteFile(exe, []byte(fakeBody), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(exe, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(plugins.SetExecutableDirForTest(dir))
	store := storetest.NewMemStore()
	ctx := context.Background()

	raw := addParams(t, "trustdemo", []byte(demoManifest))
	if _, err := addChain(store, openDomainDB(t))(ctx, raw); !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("plugin.add without a witness = %v, want KindPolicyDenied", err)
	}
	if _, ok, err := plugins.LoadMetadata(ctx, store, "trustdemo"); ok || err != nil {
		t.Fatalf("an unwitnessed add left a record (ok=%v, err=%v)", ok, err)
	}

	rec := InstallTrusted(t, store, "trustdemo", []byte(demoManifest)).Load(t)
	sum := sha256.Sum256([]byte(fakeBody))
	if rec.TrustTier != plugins.TrustTrusted || rec.ExecDigest != hex.EncodeToString(sum[:]) || !rec.Enabled {
		t.Fatalf("record after a witnessed add = %+v, want enabled, trusted, pinned to the executable", rec)
	}
	if got := plugins.EffectiveGrants(rec); len(got) != 2 {
		t.Fatalf("EffectiveGrants = %v, want the manifest's two requires", got)
	}
}

func TestPlugintestBuildGithub(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	first, second := BuildGithub(t), BuildGithub(t)
	a, err := os.ReadFile(filepath.Join(first, ExecutableName(GithubID)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(second, ExecutableName(GithubID)))
	if err != nil || first == second || string(a) != string(b) || len(a) == 0 {
		t.Fatalf("BuildGithub gave %s and %s (err %v); want two dirs holding the same binary", first, second, err)
	}
}

// fatalTB records a fatal failure instead of failing the real test.
type fatalTB struct {
	testing.TB
	failed bool
}

func (f *fatalTB) Helper() {}

func (f *fatalTB) Fatalf(string, ...any) {
	f.failed = true
	goruntime.Goexit()
}

// fails reports whether fn failed tb fatally.
func fails(t *testing.T, fn func(tb testing.TB)) bool {
	t.Helper()
	tb := &fatalTB{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(tb)
	}()
	<-done
	return tb.failed
}

func TestPlugintestRefusalsFailTheTest(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("Windows tier-2 refuses elevation before the attestation flow")
	}
	t.Cleanup(plugins.SetExecutableDirForTest(t.TempDir()))
	store := storetest.NewMemStore()
	if !fails(t, func(tb testing.TB) { PluginRecordRef{Store: store, ID: "trustdemo"}.Load(tb) }) {
		t.Fatal("Load of a missing record did not fail the test")
	}
	if !fails(t, func(tb testing.TB) { InstallTrusted(tb, store, "trustdemo", []byte(demoManifest)) }) {
		t.Fatal("InstallTrusted without an executable did not fail the test")
	}
	chain := addChain(store, openDomainDB(t))
	for _, raw := range []string{`not json`, `{"id":"x","manifest_bytes":"bm90ID0gW3RvbWw="}`} {
		if _, err := chain(context.Background(), []byte(raw)); err == nil {
			t.Fatalf("plugin.add of %s succeeded", raw)
		}
	}
	if (fixedClock{}).Now().IsZero() {
		t.Fatal("the domain clock reads zero")
	}
}
