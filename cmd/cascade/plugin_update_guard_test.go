//go:build !windows

// Purpose: `cascade plugin update` CLI guards: a --from manifest whose id is
// not <name> is refused before any store opens, and an update that needs
// elevation exits non-zero with nothing written when input is disabled or
// the elevated call is refused; the CLI itself never writes the record.
// SPORT: cmd/cascade plugin update guards.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// expandingDemoManifest updates "demo" to v1.1.0 and asks for one more grant
// than the seeded record holds.
const expandingDemoManifest = `
id = "demo"
name = "Demo"
schema = "cascade.plugin/v2"
version = "1.1.0"
host_version = ">=2.0.0"
runtime = "builtin"
requires = ["net.http", "fs.read"]
`

// spyPaths counts every path lookup; the store cannot open without one.
type spyPaths struct {
	fakeDaemonPaths
	calls *int
}

func (p spyPaths) Root() string       { *p.calls++; return p.fakeDaemonPaths.Root() }
func (p spyPaths) ConfigPath() string { *p.calls++; return p.fakeDaemonPaths.ConfigPath() }
func (p spyPaths) SocketPath() string { *p.calls++; return p.fakeDaemonPaths.SocketPath() }
func (p spyPaths) DataDir() string    { *p.calls++; return p.fakeDaemonPaths.DataDir() }
func (p spyPaths) StorageRoot(prof runtime.Profile) string {
	*p.calls++
	return p.fakeDaemonPaths.StorageRoot(prof)
}

// writeManifest writes src to a fresh temp file and returns its path.
func writeManifest(t *testing.T, src string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.toml")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// swapUpdateElevatedCall replaces the elevated dial for one test.
func swapUpdateElevatedCall(t *testing.T, fn func(context.Context, pluginDeps, io.Writer, plugins.UpdateParams, any) error) {
	t.Helper()
	prev := pluginUpdateElevatedCall
	pluginUpdateElevatedCall = fn
	t.Cleanup(func() { pluginUpdateElevatedCall = prev })
}

// recordJSON renders the CLI store's record for name.
func recordJSON(t *testing.T, deps pluginDeps, name string) string {
	t.Helper()
	b, _ := json.Marshal(loadPluginUpdateMetadata(t, deps, name))
	return string(b)
}

func seedDemoV1(t *testing.T, deps pluginDeps) {
	t.Helper()
	seedPluginUpdateMetadata(t, deps, plugins.PluginMetadata{Name: "demo", InstalledVersion: "1.0.0", Enabled: true,
		RuntimeMode: plugin.RuntimeBuiltin, Grants: []string{"net.http"}})
}

func TestPluginUpdateFromNameMismatchRefuses(t *testing.T) {
	calls := 0
	deps := testPluginDeps(t)
	deps.Paths = spyPaths{fakeDaemonPaths: fakeDaemonPaths{root: t.TempDir()}, calls: &calls}
	_, _, err := runPluginCLIState(t, deps, runtime.DaemonlessState{Embedded: false},
		"update", "other", "--from", writeManifest(t, expandingDemoManifest))
	if kind, _ := cascade.KindOf(err); kind != cascade.KindInvalidInput || !strings.Contains(errString(err), `manifest id "demo" does not match requested name "other"`) {
		t.Fatalf("err = %v, want KindInvalidInput naming both ids", err)
	}
	if calls != 0 {
		t.Fatalf("%d path lookups before the id check refused; the store must not open", calls)
	}
}

func TestPluginUpdateElevationRequiredExitsNonZero(t *testing.T) {
	t.Run("no input", testUpdateElevationNoInput)
	t.Run("refused", testUpdateElevationRefused)
	t.Run("untyped failure", testUpdateElevationUntypedFailure)
}

func testUpdateElevationNoInput(t *testing.T) {
	daemonUp := runtime.DaemonlessState{Embedded: false}
	deps := testPluginDeps(t)
	deps.Getenv = func(k string) string {
		if k == "CASCADE_NO_INPUT" {
			return "1"
		}
		return ""
	}
	seedDemoV1(t, deps)
	before := recordJSON(t, deps, "demo")
	swapUpdateElevatedCall(t, func(context.Context, pluginDeps, io.Writer, plugins.UpdateParams, any) error {
		t.Error("CASCADE_NO_INPUT=1 reached the elevated call")
		return nil
	})
	_, _, err := runPluginCLIState(t, deps, daemonUp, "update", "demo", "--from", writeManifest(t, expandingDemoManifest))
	if cascade.ExitCode(err) == 0 || !strings.Contains(errString(err), "CASCADE_NO_INPUT=1") {
		t.Fatalf("err = %v (exit %d), want the non-zero no-input refusal", err, cascade.ExitCode(err))
	}
	if after := recordJSON(t, deps, "demo"); after != before {
		t.Fatalf("record changed: %s -> %s", before, after)
	}
}

func testUpdateElevationRefused(t *testing.T) {
	daemonUp := runtime.DaemonlessState{Embedded: false}
	d := newElevatedDaemon(t, filepath.Join(t.TempDir(), "data"), false) // unenrolled: every elevated verb refuses
	seedDaemonDemoV1(t, d)                                               // an accepted elevation would commit here
	daemonBefore := installedPlugins(t, d.Store)
	deps := testPluginDeps(t)
	seedDemoV1(t, deps)
	before := recordJSON(t, deps, "demo")
	dialed := 0
	swapUpdateElevatedCall(t, func(ctx context.Context, _ pluginDeps, _ io.Writer, req plugins.UpdateParams, out any) error {
		dialed++
		return asClientError(d.Call(ctx, plugins.UpdateRPCMethod, req, out))
	})
	stdout, _, err := runPluginCLIState(t, deps, daemonUp, "update", "demo", "--from", writeManifest(t, expandingDemoManifest))
	// The daemon's own typed refusal (no enrolled key) reaches the operator unchanged.
	if kind, _ := cascade.KindOf(err); kind != cascade.KindNotFound || !strings.Contains(errString(err), "enrolled") || cascade.ExitCode(err) == 0 {
		t.Fatalf("err = %v (exit %d), want the daemon's non-zero enrolment refusal", err, cascade.ExitCode(err))
	}
	if dialed != 1 || !strings.Contains(stdout, "elevation required") {
		t.Fatalf("dialed %d times, stdout %q; want one dial after the view", dialed, stdout)
	}
	if after := recordJSON(t, deps, "demo"); after != before {
		t.Fatalf("record changed: %s -> %s", before, after)
	}
	if after := installedPlugins(t, d.Store); after != daemonBefore {
		t.Fatalf("the refused daemon changed its record:\n before %s\n after  %s", daemonBefore, after)
	}
}

func testUpdateElevationUntypedFailure(t *testing.T) {
	daemonUp := runtime.DaemonlessState{Embedded: false}
	deps := testPluginDeps(t)
	seedDemoV1(t, deps)
	swapUpdateElevatedCall(t, func(context.Context, pluginDeps, io.Writer, plugins.UpdateParams, any) error {
		return io.ErrUnexpectedEOF // a transport failure carries no kind
	})
	_, _, err := runPluginCLIState(t, deps, daemonUp, "update", "demo", "--from", writeManifest(t, expandingDemoManifest))
	if kind, _ := cascade.KindOf(err); kind != cascade.KindElevationRequired || !strings.Contains(errString(err), "update not applied") {
		t.Fatalf("err = %v, want KindElevationRequired for an untyped failure", err)
	}
}

// TestPluginUpdateElevatedKeepsDaemonRefusalKind: an attested update the
// daemon refuses on its merits (the daemon has no record of demo) keeps the
// daemon's kind and message instead of reading as an elevation failure.
func TestPluginUpdateElevatedKeepsDaemonRefusalKind(t *testing.T) {
	d := newElevatedDaemon(t, filepath.Join(t.TempDir(), "data"), true)
	swapUpdateElevatedCall(t, func(ctx context.Context, _ pluginDeps, _ io.Writer, req plugins.UpdateParams, out any) error {
		return asClientError(d.Call(ctx, plugins.UpdateRPCMethod, req, out))
	})
	deps := testPluginDeps(t)
	seedDemoV1(t, deps)
	_, _, err := runPluginCLIState(t, deps, runtime.DaemonlessState{Embedded: false}, "update", "demo", "--from", writeManifest(t, expandingDemoManifest))
	if kind, _ := cascade.KindOf(err); kind != cascade.KindNotFound || !strings.Contains(errString(err), "demo is not installed; use cascade plugin add") {
		t.Fatalf("err = %v, want the daemon's KindNotFound unchanged", err)
	}
}

// seedDaemonDemoV1 seeds the light daemon fixture's store with demo v1.0.0.
func seedDaemonDemoV1(t *testing.T, d elevatedDaemon) {
	t.Helper()
	if err := plugins.SaveMetadata(context.Background(), d.Store, plugins.PluginMetadata{Name: "demo", InstalledVersion: "1.0.0",
		Enabled: true, RuntimeMode: plugin.RuntimeBuiltin, Grants: []string{"net.http"}}); err != nil {
		t.Fatal(err)
	}
}

// asClientError maps a registry ErrorObject to the taxonomy error the real
// client returns for it (internal/client's kindForRPCError: the code's Kind,
// else KindInternal), so a test seam sees what production sees.
func asClientError(err error) error {
	var eo *rpc.ErrorObject
	if !errors.As(err, &eo) {
		return err
	}
	kind, ok := cascade.KindFromJSONRPCCode(eo.Code)
	if !ok {
		kind = cascade.KindInternal
	}
	return cascade.New(kind, eo.Message)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// TestPluginUpdateRegistryRefusesUninstalled drives the registry path (no
// --from) through the real command: an uninstalled name is KindNotFound
// naming `cascade plugin add`, and nothing is written.
func TestPluginUpdateRegistryRefusesUninstalled(t *testing.T) {
	deps := updateTestDeps(t)
	_, _, err := runPluginCLI(t, deps, "update", "grant-diff-demo")
	if kind, _ := cascade.KindOf(err); kind != cascade.KindNotFound ||
		!strings.Contains(errString(err), "grant-diff-demo is not installed; use cascade plugin add") {
		t.Fatalf("err = %v, want KindNotFound naming cascade plugin add", err)
	}
	store, closeStore, err := openPluginStore(context.Background(), deps.Paths, deps.Clock)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore()
	if recs, _ := plugins.ListMetadata(context.Background(), store); len(recs) != 0 {
		t.Fatalf("a refused registry update wrote %v", recs)
	}
}

// TestPluginUpdateRegistryRefusesWhileDaemonRuns: the registry path cannot
// read the installed version while the daemon holds the store, so it
// refuses before opening it.
func TestPluginUpdateRegistryRefusesWhileDaemonRuns(t *testing.T) {
	calls := 0
	deps := updateTestDeps(t)
	deps.Paths = spyPaths{fakeDaemonPaths: fakeDaemonPaths{root: t.TempDir()}, calls: &calls}
	_, _, err := runPluginCLIState(t, deps, runtime.DaemonlessState{Embedded: false}, "update", "grant-diff-demo")
	if kind, _ := cascade.KindOf(err); kind != cascade.KindUnavailable || !strings.Contains(errString(err), "update via --from") {
		t.Fatalf("err = %v, want the daemon-running refusal", err)
	}
	if calls != 0 {
		t.Fatalf("%d path lookups; the store must not open while the daemon runs", calls)
	}
}
