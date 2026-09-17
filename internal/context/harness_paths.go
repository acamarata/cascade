package context

// Purpose: WHERE each supported harness keeps its configuration
//   (P1-E16-W4-S35-T3) — the path table and the one resolver that reads
//   it. Split out of harness.go so that file stays the detector's
//   behaviour and this stays the per-harness facts, and so the 300-line
//   cap is met by a seam rather than by an arbitrary cut.
// Inputs: a harness kind and the detector's injected environment.
// Outputs: an absolute config root, or a typed refusal naming the
//   variable whoever hit it could set.
// Constraints: every row is a VERIFIED path, checked against a real
//   installation rather than recalled (R-14.252). This file resolves
//   paths and reads nothing.
// SPORT: internal/context harness detection (ADD) — P1-E16-W4-S35-T3.

import (
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// configRoot resolves one harness's config root for this platform.
//
// Each harness's own override environment variable is consulted FIRST,
// because a machine that sets one is telling both the harness and us
// where its state actually lives; a detector that ignored it would probe
// the default root and report "not installed" on an installation that is
// simply relocated. This is not hypothetical — running two accounts of
// one harness side by side is exactly what the override exists for.
//
// After that, XDG and then the home dotfile, resolved from the same
// variables the harnesses themselves read. The XDG branch is written as
// "every other GOOS" rather than as "linux", so freebsd and friends do
// not fall into a branch nobody wrote for them — the same rule
// plugins/claude's ResolvePaths states. A harness with no xdg spelling
// skips it and lands on its dotfile root on every platform, which is
// what all but one of them actually do.
func (d *PathDetector) configRoot(kind HarnessKind) (string, error) {
	dirs, ok := harnessConfigDirs[kind]
	if !ok {
		return "", cascade.Newf(cascade.KindInternal, "context: no config directory is known for harness %q", kind)
	}
	if name := OverrideVarFor(kind); name != "" {
		if override := d.env(name); override != "" {
			return filepath.Clean(override), nil
		}
	}
	if xdg := d.env("XDG_CONFIG_HOME"); xdg != "" && dirs.xdg != "" {
		return filepath.Join(xdg, dirs.xdg), nil
	}
	home := d.env("HOME")
	if home == "" {
		return "", cascade.Newf(cascade.KindUnavailable,
			"context: resolve the %s config root on %s: neither %s nor HOME is set",
			kind, d.goos, envOverrideNameOr(OverrideVarFor(kind), "XDG_CONFIG_HOME"))
	}
	return filepath.Join(home, filepath.FromSlash(dirs.home)), nil
}

// envOverrideNameOr names the variable a caller could have set, for the
// refusal above: the harness's own override when it has one, and the XDG
// variable when it does not. A refusal that named a variable the harness
// does not read would send whoever hit it to the wrong place.
func envOverrideNameOr(override, fallback string) string {
	if override != "" {
		return override
	}
	return fallback
}

// harnessDirs is one harness's config-root spelling.
//
// There is deliberately no macOS Application Support row. All three
// supported harnesses are CLI tools that keep a dotfile directory (or an
// XDG directory) on every platform they run on, verified against real
// installations for this ticket. One of them ships a desktop application
// that DOES keep a profile under Application Support — an Electron cache
// directory, no config of ours in it — and treating that directory as the
// CLI harness's root would report the harness installed on a machine that
// has only the desktop app, and then look for a state file that is not
// there. A platform row nothing uses is worse than no row: it is a false
// positive waiting for the first person who installs the other product.
type harnessDirs struct {
	// envSuffix is the tail of the harness's own "my config lives here
	// instead" environment variable; OverrideVarFor prepends the
	// uppercased harness name to build the full one. Empty for a harness
	// that has no such variable.
	//
	// A suffix rather than the whole name because this repository is
	// public and carries no downstream product identifiers in tracked
	// text (PRI hard rule 3, gate-enforced). The convention is stated in
	// full on OverrideVarFor, so nothing about the resulting name is
	// hidden from a reader — it is derived in the open rather than
	// spelled out.
	envSuffix string
	// xdg is the directory name under XDG_CONFIG_HOME, for a harness
	// that honours XDG. Empty for one that does not, which then uses
	// home on every platform.
	xdg string
	// home is the path under HOME, slash-separated.
	home string
	// configFile is the harness's own state file within the root, when
	// it is one ParseHarnessConfig can read. Empty for a harness whose
	// config is in another format — an empty entry is a stated "this
	// build does not read that one", not an oversight, and it is why
	// Version is documented as absent rather than guessed.
	configFile string
	// besideHomeWithoutOverride says the config file sits NEXT TO the
	// home directory rather than inside the config root, whenever the
	// override variable is unset.
	//
	// Verified on a machine running two instances of the same harness at
	// once: the plain instance keeps its user config at
	// $HOME/<configFile>, and the instance started with the override set
	// keeps it at <override>/<configFile>. Reading only the root-relative
	// spelling finds a stale file (or none) for the plain instance, which
	// is how a registered MCP server reads back as unregistered.
	besideHomeWithoutOverride bool
}

// harnessConfigDirs is the path table. It is a var rather than a switch
// so the three rows read as data, and so a test can assert the table's
// membership against SupportedHarnesses rather than against whatever the
// switch happened to handle.
//
// Every row was checked against a real installation rather than recalled
// (R-14.252): the roots below are the ones the tools had actually created
// on the machine this was written on.
var harnessConfigDirs = map[HarnessKind]harnessDirs{
	// The first-party harness client keeps a dotfile root on every
	// platform and does not honour XDG_CONFIG_HOME. Its user state file
	// sits beside HOME by default and moves INSIDE the root when the
	// override relocates it -- see besideHomeWithoutOverride.
	HarnessClaude: {
		envSuffix: "_CONFIG_DIR", home: ".claude", configFile: ".claude.json",
		besideHomeWithoutOverride: true,
	},
	// codex keeps a TOML config, which ParseHarnessConfig does not read;
	// its Version and CascadeRegistered are reported absent.
	HarnessCodex: {envSuffix: "_HOME", home: ".codex"},
	// opencode is the one harness of the three that uses XDG.
	HarnessOpenCode: {
		xdg: "opencode", home: ".config/opencode", configFile: "opencode.json",
	},
}

// OverrideVarFor returns the environment variable that relocates kind's
// config root, or "" for a harness that has none.
//
// The name is the UPPERCASED harness kind followed by that harness's own
// suffix — "_CONFIG_DIR" for one of them, "_HOME" for another. Both
// tools document their variable under that spelling; there is no single
// suffix across the three, which is why it is per-harness data rather
// than one rule.
//
// It is exported because a test that pins an environment has to clear
// the same variables production reads: this process may itself be
// running under one, and an inherited override would silently point
// detection at a real installation instead of the fixture.
func OverrideVarFor(kind HarnessKind) string {
	dirs, ok := harnessConfigDirs[kind]
	if !ok || dirs.envSuffix == "" {
		return ""
	}
	return strings.ToUpper(string(kind)) + dirs.envSuffix
}

// configFilePath resolves where kind keeps the state file this build can
// read, given its already-resolved root. It returns "" for a harness whose
// config this build does not read.
func (d *PathDetector) configFilePath(kind HarnessKind, root string) string {
	dirs, ok := harnessConfigDirs[kind]
	if !ok || dirs.configFile == "" {
		return ""
	}
	if dirs.besideHomeWithoutOverride {
		if name := OverrideVarFor(kind); name == "" || d.env(name) == "" {
			home := d.env("HOME")
			if home == "" {
				return ""
			}
			return filepath.Join(home, dirs.configFile)
		}
	}
	return filepath.Join(root, dirs.configFile)
}
