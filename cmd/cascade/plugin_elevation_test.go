package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// pluginGateFixture returns plugin deps over the signed gate fixture and a
// builtin manifest path at version.
func pluginGateFixture(t *testing.T) (pluginDeps, *gateFixture) {
	t.Helper()
	f := newGateFixture(t, nil)
	deps := testPluginDeps(t)
	deps.Gate = f.gate
	return deps, f
}

func writeBuiltinManifest(t *testing.T, version string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "demo-"+version+".toml")
	src := strings.Replace(builtinManifestFixture, `version = "1.0.0"`, `version = "`+version+`"`, 1)
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// lastGrant reports the verb of the newest elevation.grant row.
func lastGrant(f *gateFixture) string {
	for i := len(f.tap.recs) - 1; i >= 0; i-- {
		if f.tap.recs[i].Kind == "elevation.grant" {
			return f.tap.recs[i].Action
		}
	}
	return ""
}

// TestPluginWritesRequireSignedProof: the local plugin-store writes of the
// always-elevated plugin verbs (add and update --from, no daemon running)
// refuse without a signature and write nothing; with a valid proof they
// apply and the grant row names the verb. Daemon-up perms and add are the
// daemon's attested calls (gate_audit_daemon_test.go).
func TestPluginWritesRequireSignedProof(t *testing.T) {
	skipWithoutLocalElevation(t)
	deps, f := pluginGateFixture(t)
	unsigned := func([]byte) ([]byte, error) { return nil, nil }
	refused := func(label string, err error) {
		t.Helper()
		if !cascade.HasKind(err, cascade.KindElevationRequired) || !strings.Contains(err.Error(), "returned no signature") {
			t.Fatalf("%s without a signature = %v, want the unsigned refusal", label, err)
		}
	}
	info := func() string {
		out, _, err := runPluginCLI(t, deps, "info", "demo")
		if err != nil {
			return err.Error()
		}
		return out
	}

	f.ks.sign = unsigned
	_, _, err := runPluginCLI(t, deps, "add", writeBuiltinManifest(t, "1.0.0"))
	refused("add", err)
	if got := info(); !strings.Contains(got, "neither installed") {
		t.Fatalf("an unsigned add installed the plugin: %s", got)
	}
	f.ks.sign = nil
	if _, _, err := runPluginCLI(t, deps, "add", writeBuiltinManifest(t, "1.0.0")); err != nil || lastGrant(f) != "plugin.add" {
		t.Fatalf("signed add = %v, last grant %q", err, lastGrant(f))
	}

	f.ks.sign = unsigned
	_, _, err = runPluginCLI(t, deps, "update", "demo", "--from", writeBuiltinManifest(t, "1.1.0"))
	refused("update", err)
	if got := info(); !strings.Contains(got, "1.0.0") {
		t.Fatalf("an unsigned update changed the record: %s", got)
	}
	f.ks.sign = nil
	if _, _, err := runPluginCLI(t, deps, "update", "demo", "--from", writeBuiltinManifest(t, "1.1.0")); err != nil || lastGrant(f) != "plugin.update" {
		t.Fatalf("signed update = %v, last grant %q", err, lastGrant(f))
	}

}

// TestPluginWritesRefuseWithoutAGate: a nil gate refuses the write.
func TestPluginWritesRefuseWithoutAGate(t *testing.T) {
	deps := testPluginDeps(t)
	deps.Gate = nil
	_, _, err := runPluginCLI(t, deps, "add", writeBuiltinManifest(t, "1.0.0"))
	if !cascade.HasKind(err, cascade.KindElevationRequired) || !strings.Contains(err.Error(), "no elevation gate") {
		t.Fatalf("add with no gate = %v", err)
	}
}

// TestPluginWriteAuditsIntoTheOpenStore is the boundary test: with the
// production audit writer (which opens the runtime store), a signed plugin
// add must succeed while the command holds that same store open, and its
// elevation rows must land in that store's audit log.
func TestPluginWriteAuditsIntoTheOpenStore(t *testing.T) {
	skipWithoutLocalElevation(t)
	resetProcessFirewall(t)
	deps, f := pluginGateFixture(t)
	f.gate.sink = &autoGateSink{paths: deps.Paths, clock: deps.Clock}
	if _, _, err := runPluginCLI(t, deps, "add", writeBuiltinManifest(t, "1.0.0")); err != nil {
		t.Fatalf("signed add with the production audit writer = %v", err)
	}
	rows, err := withPluginStore(t.Context(), deps, func(_ context.Context, store provider.Store) (string, error) {
		joined, _ := storedAuditRecords(t, store)
		return joined, nil
	})
	if err != nil || !strings.Contains(rows, "elevation.grant") || !strings.Contains(rows, "plugin.add") {
		t.Fatalf("audit rows in the plugin store = %v %q", err, rows)
	}
}

// TestPluginEarlyRefusalsAudited: refusals decided before any store write
// leave attempt then deny with their reason.
func TestPluginEarlyRefusalsAudited(t *testing.T) {
	for label, args := range map[string][]string{
		"perms grant without a daemon":  {"perms", "grant", "demo", "fake.capability"},
		"perms revoke without a daemon": {"perms", "revoke", "demo", "fake.capability"},
	} {
		deps, f := pluginGateFixture(t)
		_, _, err := runPluginCLI(t, deps, args...)
		assertDenied(t, label, f, err, "needs a running daemon", "daemon_required")
	}
	deps, f := pluginGateFixture(t)
	proc := filepath.Join(t.TempDir(), "proc.toml")
	writeErr := os.WriteFile(proc, []byte("id = \"proc-demo\"\nname = \"Proc Demo\"\nschema = \"cascade.plugin/v2\"\n"+
		"version = \"1.0.0\"\nhost_version = \">=2.0.0\"\nruntime = \"process\"\n"), 0o600)
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	_, _, err := runPluginCLI(t, deps, "add", proc)
	assertDenied(t, "process-tier add without a daemon", f, err, "needs a running daemon", "daemon_required")
	if f.ks.signs != 0 {
		t.Fatalf("a daemon-required refusal asked for %d signatures", f.ks.signs)
	}
	// A registry update that would add a grant, refused for no input.
	deps = updateTestDeps(t)
	f = newGateFixture(t, nil)
	deps.Gate = f.gate
	seedPluginUpdateMetadata(t, deps, pluginUpdateBuiltin(plugins.PluginMetadata{Name: "grant-diff-demo", InstalledVersion: "1.0.0", Grants: []string{"storage.local"}}))
	deps.Getenv = func(k string) string { return map[string]string{"CASCADE_NO_INPUT": "1"}[k] }
	_, _, err = runPluginCLI(t, deps, "update", "grant-diff-demo")
	assertDenied(t, "registry update with no input", f, err, "CASCADE_NO_INPUT=1", "no_input")
}
