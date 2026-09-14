package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
)

// testPluginDeps builds pluginDeps against a fresh temp directory, never
// the operator's real environment. DialContext is deliberately nil: every
// test in this file either takes the embedded path or is refused (daemon-
// required, CASCADE_NO_INPUT) before a dial would ever happen — this
// package's untagged tests must never import "net" (the no-network unit
// lane, internal/build's own gate), so DialContext stays a documented
// "must not be called" nil rather than a hand-rolled net.Conn stand-in.
func testPluginDeps(t *testing.T) pluginDeps {
	t.Helper()
	return pluginDeps{
		Paths:  fakeDaemonPaths{root: t.TempDir()},
		Clock:  runtime.NewSystemClock(),
		Getenv: func(string) string { return "" },
	}
}

// runPluginCLI executes root's `plugin ...` tree with args, embedded
// (daemonless) since no probe ever runs in these unit tests, and returns
// stdout/stderr and the RunE error.
func runPluginCLI(t *testing.T, deps pluginDeps, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runPluginCLIState(t, deps, runtime.DaemonlessState{Embedded: true}, args...)
}

// runPluginCLIState is runPluginCLI with an explicit DaemonlessState, for
// the few tests (e.g. CASCADE_NO_INPUT on an elevated verb) that must
// observe the daemon-available branch without a real socket ever being
// dialed.
func runPluginCLIState(t *testing.T, deps pluginDeps, st runtime.DaemonlessState, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := newPluginCmd(deps)
	guardUnknownSubcommands(root)
	root.SetArgs(args)
	var outBuf, errBuf bytes.Buffer
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.PersistentFlags().Bool("json", false, "")
	root.PersistentFlags().Bool("quiet", false, "")
	root.PersistentFlags().Bool("verbose", false, "")
	root.PersistentFlags().Bool("no-color", false, "")
	ctx := runtime.WithDaemonlessState(context.Background(), st)
	err = root.ExecuteContext(ctx)
	return outBuf.String(), errBuf.String(), err
}

// TestPluginCLI_MountedOnRealCommandTree is the LANE-RULES §4 mutation
// proof, expressed as a standing regression test: `plugin` must be
// reachable from the SAME root.go mountSubcommands wiring the shipping
// binary uses, via guardUnknownSubcommands (which fails a command whose
// own subcommand set was never registered — see root.go). The agent
// building this ticket additionally removed the `mountPluginCmd(root)`
// line from root.go by hand, re-ran this test, and observed the real
// failure `unknown command "plugin" for "cascade"` before restoring the
// line and re-observing green — see the ticket's journal for the quoted
// transcript; this test is what caught it.
func TestPluginCLI_MountedOnRealCommandTree(t *testing.T) {
	root := newRootCmd()
	mountSubcommands(root)
	root.SetArgs([]string{"plugin", "list", "--json"})
	var out bytes.Buffer
	root.SetOut(&out)
	ctx := runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: true})
	// A fresh, otherwise-unwritable data dir: this run must fail on a
	// filesystem issue (or succeed with zero plugins), never on
	// "unknown command" — that specific failure mode is what the removed-
	// line experiment produced.
	if err := root.ExecuteContext(ctx); err != nil && strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("plugin command tree is not mounted: %v", err)
	}
}

func TestPluginCLI_ListEmptyAndInfoNotFound(t *testing.T) {
	deps := testPluginDeps(t)

	stdout, _, err := runPluginCLI(t, deps, "list", "--json")
	if err != nil {
		t.Fatalf("plugin list on an empty store: %v", err)
	}
	var env map[string]any
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &env); jerr != nil {
		t.Fatalf("plugin list --json did not emit a JSON envelope: %v (stdout=%q)", jerr, stdout)
	}

	if _, _, err := runPluginCLI(t, deps, "info", "ghost"); err == nil {
		t.Fatal("plugin info on an unknown name succeeded, want KindNotFound")
	}
}

func TestPluginCLI_AddListEnableDisableRemove_RealStoreState(t *testing.T) {
	deps := testPluginDeps(t)
	manifestPath := filepath.Join(t.TempDir(), "demo.toml")
	if err := os.WriteFile(manifestPath, []byte(builtinManifestFixture), 0o600); err != nil {
		t.Fatalf("write manifest fixture: %v", err)
	}

	if _, _, err := runPluginCLI(t, deps, "add", manifestPath); err != nil {
		t.Fatalf("plugin add: %v", err)
	}

	stdout, _, err := runPluginCLI(t, deps, "list", "--json")
	if err != nil {
		t.Fatalf("plugin list after add: %v", err)
	}
	if !strings.Contains(stdout, "\"demo\"") {
		t.Fatalf("plugin list after add does not mention the installed plugin: %s", stdout)
	}

	if _, _, err := runPluginCLI(t, deps, "disable", "demo"); err != nil {
		t.Fatalf("plugin disable: %v", err)
	}
	infoOut, _, err := runPluginCLI(t, deps, "info", "demo", "--json")
	if err != nil {
		t.Fatalf("plugin info after disable: %v", err)
	}
	if !strings.Contains(infoOut, "\"enabled\": false") {
		t.Fatalf("plugin info after disable does not show enabled=false: %s", infoOut)
	}

	if _, _, err := runPluginCLI(t, deps, "remove", "demo"); err != nil {
		t.Fatalf("plugin remove: %v", err)
	}
	if _, _, err := runPluginCLI(t, deps, "info", "demo"); err == nil {
		t.Fatal("plugin info after remove succeeded, want KindNotFound (real store state, not just an emitted event)")
	}
}

// builtinManifestFixture mirrors internal/plugins/lifecycle_add_test.go's
// builtinManifest (a duplicate, not an import: cmd/cascade cannot import
// an internal/plugins _test.go file, and re-declaring one small fixture
// string is simpler and more honest than adding a shared, exported
// production fixture just for tests).
const builtinManifestFixture = `
id = "demo"
name = "Demo"
schema = "cascade.plugin/v2"
version = "1.0.0"
host_version = ">=2.0.0"
runtime = "builtin"
`

func TestPluginCLI_AddProcessTierDaemonlessRefusal(t *testing.T) {
	deps := testPluginDeps(t)
	manifestPath := filepath.Join(t.TempDir(), "proc.toml")
	const processManifestFixture = `
id = "proc-demo"
name = "Proc Demo"
schema = "cascade.plugin/v2"
version = "1.0.0"
host_version = ">=2.0.0"
runtime = "process"
`
	if err := os.WriteFile(manifestPath, []byte(processManifestFixture), 0o600); err != nil {
		t.Fatalf("write manifest fixture: %v", err)
	}
	if _, _, err := runPluginCLI(t, deps, "add", manifestPath); err == nil {
		t.Fatal("plugin add of a process-tier manifest with no daemon succeeded, want the daemon-required refusal")
	} else if !strings.Contains(err.Error(), "daemon") {
		t.Fatalf("error = %v, want it to name the daemon requirement", err)
	}
}

func TestPluginCLI_PermsNoInputHardError(t *testing.T) {
	deps := testPluginDeps(t)
	deps.Getenv = func(k string) string {
		if k == "CASCADE_NO_INPUT" {
			return "1"
		}
		return ""
	}
	// A daemon IS reachable here (Embedded: false) so the assertion is
	// really about CASCADE_NO_INPUT, not about the (separate,
	// independently tested) daemon-required gate — ChangePerms checks
	// daemon-availability first and CASCADE_NO_INPUT second, so this
	// command never gets far enough to need the (unreachable) test dialer
	// either way.
	_, _, err := runPluginCLIState(t, deps, runtime.DaemonlessState{Embedded: false}, "perms", "grant", "demo", "net.http")
	if err == nil {
		t.Fatal("plugin perms grant with CASCADE_NO_INPUT=1 succeeded, want the hard error")
	}
	if !strings.Contains(err.Error(), "CASCADE_NO_INPUT") {
		t.Fatalf("error = %v, want it to name CASCADE_NO_INPUT", err)
	}
}
