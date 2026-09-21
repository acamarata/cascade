// Purpose: pluginCatalogSearch's fail-closed contract (R-14.93, D2/D7) —
// verified/unreachable/unverified/absent — over a REAL
// plugin.Ed25519Verifier and RegistryClient (S-50.T1's own
// throwaway-signer technique: a real in-test Ed25519 keypair, never the
// production fixture), plus the real rpc.Registry dispatch proof D3/D4
// need (nothing before this ticket ever dialed "plugin.search" through
// the actual dispatch path — s50t2-cr-verdict.txt finding 3) and the
// daemonless embedded-CLI fallback D3's txtar scenarios also exercise.
//
// LAYERING: "net"/"net/http" stay out of this file (Art.7.2) — the fake
// fetcher below is a plugin.RegistryFetcher double, never a real
// transport, matching pkg/plugin/registry_client_test.go's own
// fakeFetcher.
//
// SPORT: cli/plugin-search/ADD (P1-E24-W5-S50-T2).
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// fakeSearchFetcher is a plugin.RegistryFetcher double (mirrors
// pkg/plugin/registry_client_test.go's own fakeFetcher).
type fakeSearchFetcher struct {
	index []byte
	err   error
}

func (f fakeSearchFetcher) FetchIndex(context.Context) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.index, nil
}
func (f fakeSearchFetcher) FetchArtifact(context.Context, plugin.RegistryVersionEntry) ([]byte, error) {
	return nil, cascade.New(cascade.KindUnsupported, "not used by these tests")
}

type fixedSearchClock struct{ now time.Time }

func (c fixedSearchClock) Now() time.Time { return c.now }

// signedTestIndex builds a real Ed25519-signed index document, matching
// registry_verify.go's VerifyIndex signed-payload construction exactly.
func signedTestIndex(t *testing.T, priv ed25519.PrivateKey, entries []plugin.RegistryIndexEntry) []byte {
	t.Helper()
	entriesRaw, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal entries: %v", err)
	}
	payload, err := json.Marshal(struct {
		SchemaVersion string          `json:"schema_version"`
		Entries       json.RawMessage `json:"entries"`
	}{plugin.SchemaVersionCurrent, entriesRaw})
	if err != nil {
		t.Fatalf("marshal signed payload: %v", err)
	}
	sig := ed25519.Sign(priv, payload)
	doc, err := json.Marshal(struct {
		SchemaVersion string          `json:"schema_version"`
		Entries       json.RawMessage `json:"entries"`
		Signature     string          `json:"signature"`
	}{plugin.SchemaVersionCurrent, entriesRaw, base64.StdEncoding.EncodeToString(sig)})
	if err != nil {
		t.Fatalf("marshal index document: %v", err)
	}
	return doc
}

func registrySourcedTestEntries() []plugin.RegistryIndexEntry {
	return []plugin.RegistryIndexEntry{
		{ID: "fmt-tool", Name: "formatter", Description: "Formats source files on save", LatestVersion: "1.2.0"},
		{ID: "lint-tool", Name: "linter", Description: "Lints source files", LatestVersion: "0.9.0"},
	}
}

// TestPluginCatalogSearchVerified: a verified, reachable registry's
// matches are included ALONGSIDE the builtin set (R-14.93 — never
// registry-ONLY, never a replacement).
func TestPluginCatalogSearchVerified(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	doc := signedTestIndex(t, priv, registrySourcedTestEntries())
	client := plugin.NewRegistryClient(plugin.RegistryConfig{}, fakeSearchFetcher{index: doc},
		plugin.Ed25519Verifier{PublicKey: pub}, nil, fixedSearchClock{now: time.Unix(1000, 0)})

	// "format" matches no builtin plugin name (cascade-claude, cascade-pa,
	// ...) so this proves the registry half without relying on the
	// builtin roster staying fixed.
	matched, err := pluginCatalogSearch(context.Background(), client, "format")
	if err != nil || len(matched) != 1 || matched[0].Name != "formatter" {
		t.Fatalf("pluginCatalogSearch(format) = %+v, err=%v, want exactly the formatter entry", matched, err)
	}

	builtinOnly, err := pluginSearchBuiltinFallback("")
	if err != nil {
		t.Fatalf("pluginSearchBuiltinFallback: %v", err)
	}
	browse, err := pluginCatalogSearch(context.Background(), client, "")
	if err != nil {
		t.Fatalf("pluginCatalogSearch(browse): %v", err)
	}
	if len(browse) != len(builtinOnly)+2 {
		t.Fatalf("pluginCatalogSearch(browse) len = %d, want builtin (%d) + 2 registry entries", len(browse), len(builtinOnly))
	}
}

func TestPluginCatalogSearchUnreachablePropagates(t *testing.T) {
	client := plugin.NewRegistryClient(plugin.RegistryConfig{},
		fakeSearchFetcher{err: cascade.Wrapf(cascade.KindUnavailable, plugin.ErrRegistryHTTP, "GET %s: status %d", "https://example.invalid/index.json", 503)},
		plugin.Ed25519Verifier{}, nil, fixedSearchClock{now: time.Unix(1000, 0)})
	_, err := pluginCatalogSearch(context.Background(), client, "")
	if err == nil {
		t.Fatal("expected the registry-unreachable error to propagate, got nil")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err = %v, want KindUnavailable", err)
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("err = %v, want it to include the status code", err)
	}
}

// TestPluginCatalogSearchUnverifiedFallsBack is D7: exact equality with
// the builtin set, not merely "the two registry names are absent" — a
// mutation returning some OTHER wrong slice would still have passed the
// weaker assertion the adversarial review flagged.
func TestPluginCatalogSearchUnverifiedFallsBack(t *testing.T) {
	_, wrongPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	otherPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	// Signed with wrongPriv, verified against a DIFFERENT public key: a
	// real signature that really does not verify.
	doc := signedTestIndex(t, wrongPriv, registrySourcedTestEntries())
	client := plugin.NewRegistryClient(plugin.RegistryConfig{}, fakeSearchFetcher{index: doc},
		plugin.Ed25519Verifier{PublicKey: otherPub}, nil, fixedSearchClock{now: time.Unix(1000, 0)})

	got, err := pluginCatalogSearch(context.Background(), client, "")
	if err != nil {
		t.Fatalf("unverified index must fail closed (no error), got: %v", err)
	}
	want, err := pluginSearchBuiltinFallback("")
	if err != nil {
		t.Fatalf("pluginSearchBuiltinFallback: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unverified result = %+v,\nwant EXACTLY the builtin set = %+v", got, want)
	}
}

// TestPluginCatalogSearchAbsentFallsBack: client=nil ("not configured")
// is exact-equal to the builtin set too — the same D7 discipline.
func TestPluginCatalogSearchAbsentFallsBack(t *testing.T) {
	got, err := pluginCatalogSearch(context.Background(), nil, "")
	if err != nil {
		t.Fatalf("absent registry must fail closed, not error: %v", err)
	}
	want, err := pluginSearchBuiltinFallback("")
	if err != nil {
		t.Fatalf("pluginSearchBuiltinFallback: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("absent-registry result = %+v,\nwant EXACTLY the builtin set = %+v", got, want)
	}
}

// TestPluginSearchEmbeddedFallback proves D3's premise, redefined by D9
// (s50t2-t0-decisions-2.txt): with NO DAEMON CONFIGURED for this process
// — testPluginDeps' fresh temp dir carries neither an explicit
// [daemon].socket entry in config.toml nor an existing socket file at
// the paths provider's default location — and deps.SearchCall unset,
// `plugin search` still answers from the local catalog: the SAME
// pluginCatalogSearch path cmd/cascade/testdata/scripts/plugin_search.
// txtar exercises through the real binary, not a stub. Embedded is
// explicitly FALSE here (a live daemon WAS probed as reachable) to prove
// the branch is keyed on "daemon configured?", never on the probe's dial
// outcome — the mirror image of TestPluginSearchDaemonDown
// (plugin_search_test.go), which IS configured but has the daemon down.
func TestPluginSearchEmbeddedFallback(t *testing.T) {
	deps := testPluginDeps(t)
	stdout, stderr, err := runPluginCLIState(t, deps, runtime.DaemonlessState{Embedded: false}, "search")
	if err != nil {
		t.Fatalf("no daemon configured: unexpected error: %v (stderr=%s)", err, stderr)
	}
	if !strings.Contains(stdout, "NAME") {
		t.Fatalf("no-daemon-configured search stdout = %q, want a rendered table", stdout)
	}
}

// TestRegisterDBPathHandlersWiresPluginSearch is D10's untagged proof
// (s50t2-t0-decisions-2.txt): it drives registerDBPathHandlers
// (plugin_rpc.go) itself — the REAL daemon composition-root function,
// not wirePluginSearchHandler called directly the way
// TestPluginSearchRPCWiring below does — over a real *rpc.Registry, then
// Dispatches "plugin.search" and asserts the envelope. The confirming
// review found TestPluginSearchRPCWiring insufficient for exactly this
// reason: it bypasses registerDBPathHandlers, so it cannot pin the
// composition-root wiring a mutation (removing the
// wirePluginSearchHandler call from registerDBPathHandlers) actually
// targets. manifest=nil short-circuits wireCascadePABridge (Telegram
// bridge, not this ticket's concern) and store=storetest.NewMemStore()
// lets RegisterRecallIndexHandler and wirePluginAddHandler register too
// — closer to the daemon's real startup shape than a nil store would be,
// with no live network or real daemon process.
func TestRegisterDBPathHandlersWiresPluginSearch(t *testing.T) {
	dir := t.TempDir()
	paths := fakeDaemonPaths{root: dir}
	clock := runtime.NewSystemClock()
	store := storetest.NewMemStore()
	bus := events.New(store, clock)
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}
	dbPath := filepath.Join(paths.DataDir(), "cascade.db")

	registry := rpc.NewRegistry()
	if err := registerDBPathHandlers(context.Background(), registry, nil, paths, clock, bus, store, dbPath); err != nil {
		t.Fatalf("registerDBPathHandlers: %v", err)
	}
	if !registry.Registered("plugin.search") {
		t.Fatal(`registerDBPathHandlers did not register "plugin.search" on the real rpc.Registry`)
	}
	result, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: "plugin.search", Params: json.RawMessage(`{"q":""}`)})
	if errObj != nil {
		t.Fatalf("Dispatch: %+v", errObj)
	}
	view, ok := result.(pluginSearchView)
	if !ok {
		t.Fatalf("Dispatch result type = %T, want pluginSearchView", result)
	}
	if len(view) == 0 {
		t.Fatal("Dispatch returned zero entries; want at least the builtin catalog")
	}
}

// TestPluginSearchRPCWiring is the mutation-relevant proof (D3/D4): a
// REAL rpc.Registry, wired the same way plugin_rpc.go's
// registerDBPathHandlers wires the daemon and mcp_tools.go's
// registerMCPToolMethods wires the MCP tool process, actually dispatches
// "plugin.search" end to end. Before this ticket's rewrite, nothing in
// this suite exercised the dispatch path at all (s50t2-cr-verdict.txt
// finding 3) — the adversarial review's own mutation (removing the
// wirePluginSearchHandler call site) survived every existing test because
// of exactly that gap.
func TestPluginSearchRPCWiring(t *testing.T) {
	registry := rpc.NewRegistry()
	paths := fakeDaemonPaths{root: t.TempDir()}
	wirePluginSearchHandler(context.Background(), registry, paths, runtime.NewSystemClock())
	if !registry.Registered("plugin.search") {
		t.Fatal(`"plugin.search" is not registered on the real rpc.Registry`)
	}
	result, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: "plugin.search", Params: json.RawMessage(`{"q":""}`)})
	if errObj != nil {
		t.Fatalf("Dispatch: %+v", errObj)
	}
	view, ok := result.(pluginSearchView)
	if !ok {
		t.Fatalf("Dispatch result type = %T, want pluginSearchView", result)
	}
	if len(view) == 0 {
		t.Fatal("Dispatch returned zero entries; want at least the builtin catalog")
	}
}
