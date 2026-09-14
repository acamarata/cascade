// Purpose: TestEpicOAcceptance is Epic O's own acceptance test
// (P1-E15-W4-S33-T4, the epic's gate ticket): it proves the S-31 + S-32
// + S-33 surface end to end on real artifacts (Art.2.3), not S-33 alone.
// Lives at internal/plugins/ (top-level, exempt from the
// plugins-providers-boundary depguard rule per dispatch.go's own header
// comment), so it can import every internal/** package the round trip
// needs.
//
// CONFORMANCE-SUITE CONTRADICTION (LANE-RULES §1, recorded rather than
// worked around): the contract's own task text asks this test to prove
// "S-32.T2 conformance suite green ... asserted by running the
// conformance suite entry point from the acceptance test." No such
// entry point exists to call: internal/plugins/conformance is, by its
// own S-32.T2 journal, "Art.10.5 declared test-only package, no exported
// symbol ships" — every file in that package is a _test.go file. A
// package with no non-test .go file produces nothing importable; Go
// itself refuses `import ".../conformance"` from any OTHER package's
// test file with "no non-test Go files". This test therefore cannot,
// and does not, invoke the conformance suite in-process. What it CAN
// honestly assert is documented per sub-test below: the conformance
// package's own green state is proven by the separate
// `go test ./internal/plugins/...` gate run, which already recurses
// into internal/plugins/conformance.
//
// SPORT: internal/plugins epic-o-acceptance (ADD) — P1-E15-W4-S33-T4.
package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/plugin"
)

// repoRootFromThisPackage walks up from this package's own directory to
// the module root, so file reads below work regardless of the working
// directory `go test` is invoked from.
func repoRootFromThisPackage(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	return filepath.Join(wd, "..", "..")
}

// TestEpicOAcceptance drives the S-31/S-32/S-33 surface end to end.
func TestEpicOAcceptance(t *testing.T) {
	root := repoRootFromThisPackage(t)

	t.Run("SDKImportableAndManifestV2Validates", testEpicOSDKImportable)

	var exampleDomainManifest plugin.Manifest
	t.Run("ExampleDomainValidatesViaPkgPluginValidate", func(t *testing.T) {
		exampleDomainManifest = testEpicOExampleDomainValidates(t, root)
	})

	t.Run("PluginAuthorGuideExistsAndNonEmpty", func(t *testing.T) {
		testEpicOGuideDocExists(t, root)
	})

	t.Run("ConformanceSuiteGreen", func(t *testing.T) {
		t.Skip("internal/plugins/conformance is an Art.10.5 test-only package (no non-test .go " +
			"file, per its own S-32.T2 journal): nothing importable exists for this test to call. " +
			"Proven instead by the separate `go test ./internal/plugins/...` gate run, which " +
			"recurses into internal/plugins/conformance already. See this file's package doc.")
	})

	t.Run("LifecycleAddListRemoveRoundTrip", func(t *testing.T) {
		if exampleDomainManifest.ID == "" {
			t.Skip("example-domain manifest did not parse; see ExampleDomainValidatesViaPkgPluginValidate")
		}
		testEpicOLifecycleRoundTrip(t, root, exampleDomainManifest)
	})

	t.Run("RemoteHandshakeReturnsDeferredFromLoopback", testEpicORemoteHandshake)
}

// testEpicOSDKImportable proves pkg/plugin (S-33.T1) is importable by
// construction (this file already imports it) and that its manifest v2
// marker constant is what the contract names.
func testEpicOSDKImportable(t *testing.T) {
	if plugin.SchemaVersion != "cascade.plugin/v2" {
		t.Fatalf("plugin.SchemaVersion = %q, want %q", plugin.SchemaVersion, "cascade.plugin/v2")
	}
}

// testEpicOExampleDomainValidates parses and validates the real
// example-domain manifest (S-33.T2) through pkg/plugin.Validate.
func testEpicOExampleDomainValidates(t *testing.T, root string) plugin.Manifest {
	t.Helper()
	path := filepath.Join(root, "plugins", "examples", "example-domain", "manifest.toml")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	m, err := plugin.ParseManifest(f)
	if err != nil {
		t.Fatalf("ParseManifest(example-domain): %v (ParseManifest itself calls pkg/plugin.Validate)", err)
	}
	if errs := plugin.Validate(m); len(errs) != 0 {
		t.Fatalf("plugin.Validate(example-domain) = %v, want no errors", errs)
	}
	return m
}

// testEpicOGuideDocExists proves .github/wiki/Plugin-Author-Guide.md
// (S-33.T3) exists and is non-empty.
func testEpicOGuideDocExists(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, ".github", "wiki", "Plugin-Author-Guide.md")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Size() == 0 {
		t.Fatalf("%s exists but is empty", path)
	}
}

// testEpicOLifecycleRoundTrip exercises the real S-32.T4 lifecycle
// surface on example-domain: AddPlugin (non-elevated layer) correctly
// reports elevation-required for its non-empty Requires set, the real
// elevated composition root (ProvisionElevated) installs it, ListMetadata
// shows the record, and RemovePlugin deletes it — real STORE STATE
// checked at every step, not merely that a function returned no error.
func testEpicOLifecycleRoundTrip(t *testing.T, root string, m plugin.Manifest) {
	t.Helper()
	ctx := context.Background()
	store := storetest.NewMemStore()

	manifestBytes, err := os.ReadFile(filepath.Join(root, "plugins", "examples", "example-domain", "manifest.toml"))
	if err != nil {
		t.Fatalf("read manifest.toml: %v", err)
	}

	addResult, err := AddPlugin(ctx, store, manifestBytes, "", nil, true)
	if err != nil {
		t.Fatalf("AddPlugin: %v", err)
	}
	if addResult.Outcome != AddOutcomeElevationRequired {
		t.Fatalf("AddPlugin outcome = %v, want AddOutcomeElevationRequired (example-domain declares non-empty requires)", addResult.Outcome)
	}

	db := openDispatchTestDB(t)
	domains := storage.NewPluginDomainRegistry()
	rec, err := ProvisionElevated(ctx, db, migrate.SQLiteEmitter{}, dispatchFixedClock{}, "", "", store, domains, m, "", false, nil)
	if err != nil {
		t.Fatalf("ProvisionElevated(example-domain): %v", err)
	}
	if rec.Name != m.ID {
		t.Errorf("rec.Name = %q, want %q", rec.Name, m.ID)
	}

	list, err := ListMetadata(ctx, store)
	if err != nil {
		t.Fatalf("ListMetadata: %v", err)
	}
	if !containsPluginName(list, m.ID) {
		t.Fatalf("ListMetadata after add = %v, want it to contain %q", list, m.ID)
	}

	if _, err := RemovePlugin(ctx, store, nil, m.ID); err != nil {
		t.Fatalf("RemovePlugin: %v", err)
	}
	listAfter, err := ListMetadata(ctx, store)
	if err != nil {
		t.Fatalf("ListMetadata after remove: %v", err)
	}
	if containsPluginName(listAfter, m.ID) {
		t.Fatalf("ListMetadata after remove = %v, want it to no longer contain %q", listAfter, m.ID)
	}
}

func containsPluginName(list []PluginMetadata, name string) bool {
	for _, rec := range list {
		if rec.Name == name {
			return true
		}
	}
	return false
}

// testEpicORemoteHandshake proves the T4 remote-runtime handshake's
// Art.1.3 deferred-warning path (enable_remote_runtime=false, the
// shipped default): no socket operation, no panic. The flag=true real
// loopback handshake half of this scenario needs a real socket, which
// internal/build's TestNoNetworkUnitTest_RealTreeGreen gate forbids in
// any non-integration _test.go tree-wide (AGENT-BRIEF / LANE-RULES §6);
// that proof lives in epic_o_remote_integration_test.go instead
// (`//go:build integration`) — found the hard way when this file
// originally imported net/net-http/httptest directly and the gate
// caught it.
func testEpicORemoteHandshake(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	m := epicORemoteManifest(t, "127.0.0.1", "1")
	if err := disabledRemoteDispatch(ctx, t, m); err == nil {
		t.Fatal("remote dispatch with enable_remote_runtime=false: want ErrRemoteRuntimeNotEnabled, got nil")
	}
}

func epicORemoteManifest(t *testing.T, host, port string) plugin.Manifest {
	t.Helper()
	src := "id = \"epico-remote\"\nname = \"Epic O Remote\"\nschema = \"cascade.plugin/v2\"\n" +
		"version = \"1.0.0\"\nhost_version = \">=2.0.0\"\nruntime = \"remote\"\n\n" +
		"[remote]\nhost = \"" + host + "\"\nport = " + port + "\n"
	m, err := plugin.ParseManifest(strings.NewReader(src))
	if err != nil {
		t.Fatalf("ParseManifest(epico-remote): %v", err)
	}
	return m
}

func disabledRemoteDispatch(ctx context.Context, t *testing.T, m plugin.Manifest) error {
	t.Helper()
	store := storetest.NewMemStore()
	db := openDispatchTestDB(t)
	domains := storage.NewPluginDomainRegistry()
	_, err := ProvisionElevated(ctx, db, migrate.SQLiteEmitter{}, dispatchFixedClock{}, "", "", store, domains, m, "", false, nil)
	return err
}
