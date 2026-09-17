package plugins

import (
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/context/hydration"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
)

// TestHydrationHookPackInstallGated is R-16.6a's kill switch: enabled
// gates INSTALLATION. With it false the descriptor never reaches the
// harness's hook config, so no hook runs at all — which is a different
// thing from a hook that runs and declines, and the difference is what
// makes it a kill switch rather than a preference.
//
// Both directions, against a registry of this test's own, so the
// assertion does not depend on what the package's init happened to
// register.
func TestHydrationHookPackInstallGated(t *testing.T) {
	cfg := hydration.Default()
	if !cfg.Enabled {
		t.Fatal("the default config is not enabled; the gate below asserts nothing")
	}
	if registered := RegisterHydrationPack(cfg); !registered {
		t.Fatal("an enabled configuration did not register the pack")
	}
	rendered := hookpacks.DefaultRegistry.Render("/tmp/cascade.sock")
	if rendered == nil {
		t.Fatal("the registry declined to render")
	}
	var cfgDoc struct {
		Hooks map[string][]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(rendered, &cfgDoc); err != nil {
		t.Fatalf("rendered config is not valid JSON: %v", err)
	}
	entries := cfgDoc.Hooks[string(hookpacks.EventUserPromptSubmit)]
	if len(entries) == 0 {
		t.Fatalf("the rendered config has no UserPromptSubmit entry:\n%s", rendered)
	}

	cfg.Enabled = false
	if registered := RegisterHydrationPack(cfg); registered {
		t.Fatal("a disabled configuration registered the pack anyway")
	}
}

// TestTheHydrationDescriptorCarriesItsTimeout pins the harness-side bound.
// The sessions pack's entries are fire-and-forget POSTs the harness does
// not wait on; this one it DOES wait on, and a descriptor that inherited
// the harness default would let a wedged hook hold a user's prompt open
// for a minute.
func TestTheHydrationDescriptorCarriesItsTimeout(t *testing.T) {
	pack := hookpacks.HydrationPack()
	if len(pack.Descriptors) != 1 {
		t.Fatalf("the pack has %d descriptors, want exactly one", len(pack.Descriptors))
	}
	d := pack.Descriptors[0]
	if d.EventType != hookpacks.EventUserPromptSubmit {
		t.Errorf("event type = %q", d.EventType)
	}
	if d.TimeoutSeconds != hookpacks.HydrationTimeoutSeconds {
		t.Errorf("timeout = %d, want %d", d.TimeoutSeconds, hookpacks.HydrationTimeoutSeconds)
	}

	rendered, err := pack.Render("/tmp/cascade.sock")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	var entry struct {
		Hooks []struct {
			Command string `json:"command"`
			Timeout int    `json:"timeout"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(rendered[0], &entry); err != nil {
		t.Fatalf("rendered entry is not valid JSON: %v", err)
	}
	if len(entry.Hooks) != 1 || entry.Hooks[0].Timeout != hookpacks.HydrationTimeoutSeconds {
		t.Fatalf("rendered timeout = %+v, want %d on the wire", entry.Hooks, hookpacks.HydrationTimeoutSeconds)
	}
	if entry.Hooks[0].Command != hookpacks.HydrationCommand {
		t.Errorf("command = %q, want %q", entry.Hooks[0].Command, hookpacks.HydrationCommand)
	}
}

// TestTheHydrationCommandIsTheInstalledBinary pins P/S-34.T1's resolution
// rule: a hook command naming an absolute path into a source or build
// tree is the v1 failure that rule exists to prevent, since it breaks the
// moment the tree moves or the build directory is cleaned.
func TestTheHydrationCommandIsTheInstalledBinary(t *testing.T) {
	cmd := hookpacks.HydrationCommand
	if len(cmd) == 0 || cmd[0] == '/' || cmd[0] == '.' {
		t.Fatalf("hook command %q is a path, not a PATH-resolved binary name", cmd)
	}
	if want := "cascade "; len(cmd) < len(want) || cmd[:len(want)] != want {
		t.Fatalf("hook command %q does not invoke the installed cascade binary", cmd)
	}
}
