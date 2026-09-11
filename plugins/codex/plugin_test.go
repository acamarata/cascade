// Purpose: real behavioral coverage for cascade-codex's registration and
//
//	dispatch surface: compile-time registration into plugin.Builtins(),
//	the manifest shape, and DispatchTool/DispatchIntent/RunCommand's
//	real (non-stub) error paths for unrecognized names.
//
// Inputs: none (pure, stateless handlers).
// Outputs: pass/fail via testing.T.
// Constraints: package-internal test; no network, no real filesystem
//
//	beyond t.TempDir() (Art.7).
//
// SPORT: plugins/codex (TEST) — P1-E16-W4-S34-T3.
package codex

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
)

func TestRegistration(t *testing.T) {
	regs := plugin.Builtins()

	var found *plugin.BuiltinRegistration
	for i := range regs {
		if regs[i].Manifest.ID == pluginID {
			found = &regs[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("plugin.Builtins() does not contain %q; init() did not register it", pluginID)
	}
	if found.Manifest.Runtime != plugin.RuntimeBuiltin {
		t.Errorf("Runtime = %q, want %q", found.Manifest.Runtime, plugin.RuntimeBuiltin)
	}
	wantCmds := map[string]bool{cmdDetect: true, cmdInstall: true, cmdUninstall: true}
	if len(found.Manifest.Provides.Commands) != len(wantCmds) {
		t.Fatalf("Provides.Commands = %d entries, want %d", len(found.Manifest.Provides.Commands), len(wantCmds))
	}
	for _, c := range found.Manifest.Provides.Commands {
		if !wantCmds[c.Name] {
			t.Errorf("unexpected command %q", c.Name)
		}
	}
	if errs := plugin.Validate(found.Manifest); len(errs) > 0 {
		t.Errorf("manifest fails Validate: %v", errs)
	}
}

func TestDispatchToolUnsupported(t *testing.T) {
	_, err := (handlers{}).DispatchTool(context.Background(), "anything", nil)
	if err == nil {
		t.Fatal("DispatchTool: want error, got nil")
	}
	if !strings.Contains(err.Error(), "no such tool") {
		t.Errorf("DispatchTool error = %q, want it to mention 'no such tool'", err.Error())
	}
}

func TestDispatchIntentUnsupported(t *testing.T) {
	_, err := (handlers{}).DispatchIntent(context.Background(), "anything", nil)
	if err == nil {
		t.Fatal("DispatchIntent: want error, got nil")
	}
	if !strings.Contains(err.Error(), "no such intent") {
		t.Errorf("DispatchIntent error = %q, want it to mention 'no such intent'", err.Error())
	}
}

func TestRunCommandUnknown(t *testing.T) {
	err := (handlers{}).RunCommand(context.Background(), "bogus", nil)
	if err == nil {
		t.Fatal("RunCommand(bogus): want error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("RunCommand error = %q, want it to mention 'unknown command'", err.Error())
	}
}

func TestRunCommandDetect(t *testing.T) {
	prev := lookPath
	defer func() { lookPath = prev }()
	lookPath = func(string) (string, error) { return "/usr/bin/codex", nil }

	if err := (handlers{}).RunCommand(context.Background(), cmdDetect, nil); err != nil {
		t.Fatalf("RunCommand(detect) = %v, want nil", err)
	}
}

// TestRunCommandInstallAndUninstall drives RunCommand's install and
// uninstall branches (not just Install/Uninstall directly), proving the
// full dispatch path from a manifest-declared command name through to the
// filesystem.
func TestRunCommandInstallAndUninstall(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/AGENTS.md"

	prevGen := Generate
	Generate = func(context.Context, string) ([]GeneratedFile, error) {
		return []GeneratedFile{{Path: path, Content: []byte("hi\n")}}, nil
	}
	defer func() { Generate = prevGen }()

	prevWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(prevWd) }()

	if err := (handlers{}).RunCommand(context.Background(), cmdInstall, nil); err != nil {
		t.Fatalf("RunCommand(install) = %v, want nil", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist after RunCommand(install): %v", path, err)
	}

	if err := (handlers{}).RunCommand(context.Background(), cmdUninstall, nil); err != nil {
		t.Fatalf("RunCommand(uninstall) = %v, want nil", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s removed after RunCommand(uninstall), stat err = %v", path, err)
	}
}
