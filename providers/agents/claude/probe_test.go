//go:build !windows

package claude

// Purpose: the init-event refusals (plugin load errors, MCP servers that
//   are not connected, permission-mode mismatch) and the probe's own
//   failure paths (no init line, hang past the timeout), each proving the
//   job never ran and the probe group was killed.
// Constraints: SCOPE DEVIATION (declared): not in files_scope.add; tests
//   probe.go, itself a declared split.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

// spawnRefused spawns and requires err to match want (by errors.Is and a
// message fragment) with the job never started.
func spawnRefused(t *testing.T, f *fx, cfg Config, spec provider.AgentJobSpec, want error, msg string) {
	t.Helper()
	_, err := f.driver(cfg).Spawn(context.Background(), spec)
	if !errors.Is(err, want) || !strings.Contains(err.Error(), msg) {
		t.Fatalf("Spawn = %v, want %v naming %q", err, want, msg)
	}
	if exists(f.jobRan) {
		t.Fatal("the job ran although the init probe refused")
	}
}

// TestHeadlessAcceptsHarnessPluginSourceKey replays the plugins member of a
// real 2.1.288 init event: its builtin entries carry a string source key.
func TestHeadlessAcceptsHarnessPluginSourceKey(t *testing.T) {
	f := newFx(t)
	var capture struct {
		Plugins []any `json:"plugins"`
	}
	if err := json.Unmarshal(readTestdata(t, "protocol/1/init_plugins_2_1_288.json"), &capture); err != nil || len(capture.Plugins) != 3 {
		t.Fatalf("capture: %v (%d plugins)", err, len(capture.Plugins))
	}
	f.initDefault = initLine(t, func(m map[string]any) { m["permissionMode"] = "default"; m["plugins"] = capture.Plugins })
	if _, _, err := run(t, f.driver(f.config()), f.spec()); err != nil {
		t.Fatalf("Spawn refused the real 2.1.288 plugin entries: %v", err)
	}
}

// pluginLoadRefusals are the init-event mutations that must refuse with
// ErrPluginLoad.
func pluginLoadRefusals() map[string]func(map[string]any) {
	return map[string]func(map[string]any){
		"plugin entry with an unknown key": func(m map[string]any) {
			m["plugins"] = []any{map[string]any{"name": "p", "path": "/x", "source": "s", "hooks": "h"}}
		},
		"plugin path null": func(m map[string]any) {
			m["plugins"] = []any{map[string]any{"name": "p", "path": nil}}
		},
		"plugin source null": func(m map[string]any) {
			m["plugins"] = []any{map[string]any{"name": "p", "path": "/x", "source": nil}}
		},
		"plugin source not a string": func(m map[string]any) {
			m["plugins"] = []any{map[string]any{"name": "p", "path": "/x", "source": 7}}
		},
		"plugin entry with an error": func(m map[string]any) {
			m["plugins"] = []any{map[string]any{"name": "p", "path": "/x", "error": "load failed"}}
		},
		"plugin_errors key":     func(m map[string]any) { m["plugin_errors"] = []any{"boom"} },
		"mcp_server_errors key": func(m map[string]any) { m["mcp_server_errors"] = map[string]any{"s": "boom"} },
		"plugins not a list":    func(m map[string]any) { m["plugins"] = "broken" },
		"plugin without a name": func(m map[string]any) { m["plugins"] = []any{map[string]any{"name": "", "path": "/x"}} },
	}
}

func TestHeadlessRefusesOnPluginLoadError(t *testing.T) {
	t.Run("real capture: connectors needing auth", func(t *testing.T) {
		f := newFx(t)
		var raw map[string]any
		_ = json.Unmarshal(readTestdata(t, "claude_headless_init.json"), &raw)
		f.initDefault = initLine(t, func(m map[string]any) { m["permissionMode"] = "default"; m["mcp_servers"] = raw["mcp_servers"] })
		spawnRefused(t, f, f.config(), f.spec(), ErrMCPServerUnavailable, `"needs-auth"`)
	})
	for name, mutate := range pluginLoadRefusals() {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			f.initDefault = initLine(t, func(m map[string]any) { m["permissionMode"] = "default"; mutate(m) })
			spawnRefused(t, f, f.config(), f.spec(), ErrPluginLoad, "plugin")
		})
	}
	t.Run("plugin dir loaded nothing", func(t *testing.T) {
		f := newFx(t)
		cfg := f.config()
		cfg.Defaults.PluginDir = filepath.Join(f.dir, "plugins")
		spawnRefused(t, f, cfg, f.spec(), ErrPluginLoad, "no plugin loaded")
	})
	t.Run("plugin dir loaded", func(t *testing.T) {
		f := newFx(t)
		cfg := f.config()
		cfg.Defaults.PluginDir = filepath.Join(f.dir, "plugins")
		f.initDefault = initLine(t, func(m map[string]any) {
			m["permissionMode"] = "default"
			m["plugins"] = []any{map[string]any{"name": "cascade", "path": filepath.Join(cfg.Defaults.PluginDir, "cascade")}}
		})
		cfg.Binary = f.script()
		if _, _, err := run(t, f.driver(cfg), f.spec()); err != nil {
			t.Fatalf("Collect: %v", err)
		}
		if !hasPair(argvOf(t, f), "--plugin-dir", cfg.Defaults.PluginDir) {
			t.Fatal("--plugin-dir missing from the job argv")
		}
	})
}

func TestInitPermissionModeMustMatchPolicy(t *testing.T) {
	f := newFx(t)
	f.initDefault = f.initBypass // a settings file forcing bypass under policy none
	spawnRefused(t, f, f.config(), f.spec(), provider.ErrHarnessIncompatible, "does not match tool policy")
	f2 := newFx(t)
	spec := f2.spec()
	spec.ToolPolicy = provider.ToolPolicyAll
	if _, _, err := run(t, f2.driver(f2.config()), spec); err != nil {
		t.Fatalf("policy all with a bypass init: %v", err)
	}
	f3 := newFx(t)
	f3.initBypass = f3.initDefault
	spawnRefused(t, f3, f3.config(), spec3(f3), provider.ErrHarnessIncompatible, "does not match tool policy")
}

func spec3(f *fx) provider.AgentJobSpec {
	s := f.spec()
	s.ToolPolicy = provider.ToolPolicyAll
	return s
}

func TestProbeFailures(t *testing.T) {
	t.Run("no init line", func(t *testing.T) {
		f := newFx(t)
		f.initDefault = []byte(`{"type":"assistant"}`)
		cfg := f.config()
		cfg.ProbeTimeout = 2 * time.Second
		spawnRefused(t, f, cfg, f.spec(), provider.ErrHarnessIncompatible, "no system/init event")
	})
	t.Run("hang past the timeout", func(t *testing.T) {
		f := newFx(t)
		pidFile := filepath.Join(f.dir, "probe-pid")
		f.versionBlock = "echo $$ > " + pidFile + "; exec sleep 30"
		cfg := f.config()
		cfg.ProbeTimeout = 300 * time.Millisecond
		spawnRefused(t, f, cfg, f.spec(), provider.ErrHarnessIncompatible, "timed out")
		pid := readPid(t, pidFile)
		eventually(t, "the timed-out probe to die", func() bool { return !alive(pid) })
	})
	t.Run("binary removed after New", func(t *testing.T) {
		f := newFx(t) // New refuses a missing binary; one deleted later fails the probe start
		cfg := f.config()
		d := f.driver(cfg)
		if err := os.Remove(cfg.Binary); err != nil {
			t.Fatal(err)
		}
		_, err := d.Spawn(context.Background(), f.spec())
		if !errors.Is(err, provider.ErrHarnessIncompatible) || !strings.Contains(err.Error(), "starting the harness probe") {
			t.Fatalf("Spawn = %v, want the probe-start refusal", err)
		}
	})
}

func TestInitShapeAndRefusalUnits(t *testing.T) {
	var ev initEvent
	if revision1Init([]byte("not json"), &ev) {
		t.Fatal("non-JSON passed the revision-1 shape")
	}
	good := initLine(t, func(m map[string]any) { m["permissionMode"] = "default" })
	if !revision1Init(good, &ev) {
		t.Fatal("the committed init capture fails the revision-1 shape")
	}
	for _, s := range []string{"", "null", "[]", "{}", " null "} {
		if nonEmptyJSON(json.RawMessage(s)) {
			t.Fatalf("nonEmptyJSON(%q) = true", s)
		}
	}
	if !nonEmptyJSON(json.RawMessage(`[1]`)) {
		t.Fatal("nonEmptyJSON([1]) = false")
	}
}
