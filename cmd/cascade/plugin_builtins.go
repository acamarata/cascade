// Purpose: make `cascade plugin …` see the plugins this binary SHIPS, not
// only the ones somebody installed into the store.
//
// WHAT WAS WRONG. `cascade init` lists the builtin plugins and its summary
// card names them; `cascade plugin list` then said "no plugins are
// installed", `plugin info pbd` said pbd was not installed, and `cascade
// pbd` worked perfectly. One product told an operator two contradictory
// things about the same five plugins, and the truthful one was the
// command that did the work. Found by the W-4 hardening gate exercising
// the shipped artifact (R-14.277).
//
// init_adapters.go's own comment already named this surface as where a
// builtin registry problem is diagnosed; it could not diagnose anything,
// because it never looked.
//
// WHAT A BUILTIN IS. Compiled into the binary, always present, always
// active: there is no install step, no version to pin, no checksum, and
// nothing that reads an enabled flag for one — root.go mounts its
// namespace unconditionally. So `enable`/`disable` on a builtin is
// REFUSED with that fact rather than with "not installed", which was
// false.
//
// SPORT: cmd/cascade/plugin builtins (ADD) — P1-E19-W4-S42-T7.
package main

import (
	"sort"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/pkg/cascade"
)

// builtinPluginRow is one compiled-in plugin, in the shape the list view
// renders installed ones.
type builtinPluginRow struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Runtime string `json:"runtime"`
}

// builtinPluginRows reads the process's own builtin registry.
//
// A registry that will not load contributes NO rows and says so through
// the returned error rather than pretending the binary ships nothing:
// "this build has no builtins" and "this build's builtins are broken" are
// different facts, and only one of them is a reason to call support.
func builtinPluginRows() ([]builtinPluginRow, error) {
	reg := &plugins.BuiltinRegistry{}
	loadErr := reg.Load()
	registered := reg.List()
	out := make([]builtinPluginRow, 0, len(registered))
	for _, r := range registered {
		out = append(out, builtinPluginRow{
			Name:    r.Manifest.ID,
			Version: r.Manifest.Version,
			Runtime: string(r.Manifest.Runtime),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, loadErr
}

// isBuiltinPlugin reports whether name is compiled into this binary.
func isBuiltinPlugin(name string) bool {
	_, ok := builtinPluginByName(name)
	return ok
}

// errBuiltinNotToggleable refuses enable/disable on a compiled-in plugin.
//
// A truthful refusal. The old one said the plugin was not installed, which
// an operator could act on — by trying to install it — and which was not
// true of a plugin whose commands were already working.
func errBuiltinNotToggleable(verb, name string) error {
	return cascade.Newf(cascade.KindUnsupported,
		"plugin: %q is built into this binary: it is always present and always active, so it "+
			"cannot be %sd. `cascade plugin list` shows it as builtin", name, verb)
}

// builtinPluginByName resolves one compiled-in plugin.
func builtinPluginByName(name string) (builtinPluginRow, bool) {
	rows, _ := builtinPluginRows()
	for _, r := range rows {
		if r.Name == name {
			return r, true
		}
	}
	return builtinPluginRow{}, false
}

// String renders `plugin info` for a builtin.
func (r builtinPluginRow) String() string {
	return r.Name + " " + r.Version + " (builtin, runtime " + r.Runtime + ")\n" +
		"built into this binary: always present, always active, and not removable"
}
