package claude

import (
	"context"
	"os"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
)

// TestManifestIsValid proves this plugin's manifest passes the host's own
// validator, so BuiltinRegistry.Load indexes it rather than rejecting it.
// A manifest that only "looks right" and is rejected at load time is a
// plugin that silently does not exist.
func TestManifestIsValid(t *testing.T) {
	if errs := plugin.Validate(manifest()); len(errs) > 0 {
		t.Fatalf("manifest rejected by the host validator: %v", errs)
	}
}

// TestPluginIsRegisteredWithTheHost proves the init() registration actually
// reaches plugin.Builtins(). Declaring a manifest without registering it is
// the "built, tested, unreachable" failure this check exists to prevent.
func TestPluginIsRegisteredWithTheHost(t *testing.T) {
	for _, reg := range plugin.Builtins() {
		if reg.Manifest.ID == pluginID {
			if reg.Handlers == nil {
				t.Fatal("registered with nil handlers")
			}
			return
		}
	}
	t.Fatalf("%s is not in plugin.Builtins(); the init() registration never ran", pluginID)
}

// TestManifestMatchesManifestTOML proves the authored TOML and the in-code
// manifest have not drifted apart. RegisterBuiltin reads the Go value, so
// the TOML is documentation — and documentation that disagrees with the
// code is worse than none.
func TestManifestMatchesManifestTOML(t *testing.T) {
	raw, err := os.ReadFile("manifest.toml")
	if err != nil {
		t.Fatalf("read manifest.toml: %v", err)
	}
	toml := string(raw)
	m := manifest()
	for _, want := range []string{
		`id = "` + m.ID + `"`,
		`name = "` + m.Name + `"`,
		`schema = "` + m.Schema + `"`,
		`version = "` + m.Version + `"`,
		`host_version = "` + m.HostVersion + `"`,
		`runtime = "` + string(m.Runtime) + `"`,
	} {
		if !strings.Contains(toml, want) {
			t.Errorf("manifest.toml is missing %s", want)
		}
	}
	for _, cmd := range m.Provides.Commands {
		if !strings.Contains(toml, `name = "`+cmd.Name+`"`) {
			t.Errorf("manifest.toml does not declare the %q command", cmd.Name)
		}
	}
	for _, perm := range m.Permissions {
		if !strings.Contains(toml, `name = "`+perm.Name+`"`) {
			t.Errorf("manifest.toml does not declare the %q permission", perm.Name)
		}
	}
}

// TestDeclaredPermissions pins the three permissions the contract names, so
// a capability added later cannot quietly ship without its consent entry.
func TestDeclaredPermissions(t *testing.T) {
	got := map[string]bool{}
	for _, perm := range manifest().Permissions {
		got[perm.Name] = true
	}
	for _, want := range []string{"read-context", "hook-emit", "mcp-register"} {
		if !got[want] {
			t.Errorf("manifest does not declare the %q permission", want)
		}
	}
}

// TestNoHarnessInstallCommand pins R-14.51: this plugin exposes no
// `harness install` verb, and its uninstall is a plugin-lifecycle command
// rather than a `cascade harness uninstall` one.
func TestNoHarnessInstallCommand(t *testing.T) {
	for _, cmd := range manifest().Provides.Commands {
		if cmd.Name == "harness-install" {
			t.Fatalf("manifest declares %q; R-14.51 strikes a harness install verb", cmd.Name)
		}
	}
}

// TestDispatchToolAndIntentAreRealRefusals proves the two unimplemented
// dispatch surfaces return genuine "no such thing" errors rather than
// silently succeeding with a zero value (an Article-1 stub).
func TestDispatchToolAndIntentAreRealRefusals(t *testing.T) {
	h := handlers{}
	if _, err := h.DispatchTool(context.Background(), "anything", nil); err == nil {
		t.Fatal("DispatchTool succeeded, want a refusal: this plugin declares no tools")
	}
	if _, err := h.DispatchIntent(context.Background(), "anything", nil); err == nil {
		t.Fatal("DispatchIntent succeeded, want a refusal: this plugin declares no intents")
	}
}

func TestRunCommandRejectsUnknownName(t *testing.T) {
	err := handlers{}.RunCommand(context.Background(), "no-such-command", nil)
	if err == nil {
		t.Fatal("RunCommand accepted an unknown command, want an error")
	}
	if !strings.Contains(err.Error(), "no-such-command") {
		t.Fatalf("error = %v, want it to name the unknown command", err)
	}
}

// TestHostPathsRefusesWindows proves the single tier-2 gate every
// host-facing entry point passes through. Driving goos explicitly means
// this branch is asserted on every platform, not only when CI happens to
// run the Windows lane.
func TestHostPathsRefusesWindows(t *testing.T) {
	_, err := hostPathsFor("windows", envMap(map[string]string{"APPDATA": `C:\x`}))
	if err == nil {
		t.Fatal("hostPathsFor(windows) succeeded, want the tier-2 refusal")
	}
	if !strings.Contains(err.Error(), "Windows tier-2") {
		t.Fatalf("error = %v, want it to name the tier", err)
	}
	if _, err := hostPathsFor("darwin", envMap(map[string]string{"HOME": "/Users/x"})); err != nil {
		t.Fatalf("hostPathsFor(darwin): %v", err)
	}
}

// runtimeIsWindows reports whether the tests are running on Windows. It
// exists so the per-platform assertions read the same way in every test
// file without each importing the stdlib runtime package under an alias.
func runtimeIsWindows() bool { return goruntime.GOOS == "windows" }
