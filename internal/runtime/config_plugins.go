// Purpose: the `[plugins]` table parser behind Load (config.go):
// 08-INIT-CONFIG-SPEC.md §3's enable_remote_runtime scalar
// (P1-E15-W4-S33-T4, R-14.48). Split out as its own sibling file per
// R-14.117's established remedy — config_widget.go and
// config_economics.go do the same, rather than growing config.go past
// its 300-line cap.
//
// FILES_SCOPE DEVIATION (recorded per LANE-RULES §1, same reasoning
// config_widget.go's own header already states for itself): the ticket's
// files_scope names "internal/runtime/config/schema.go" as the file to
// change. No such path exists in this tree — there is no
// internal/runtime/config/ subpackage at all; every config section lives
// directly in internal/runtime as a sibling config_*.go file
// (config.go, config_widget.go, config_economics.go, config_fleet.go).
// This file follows that already-shipped convention rather than
// inventing a new subpackage the rest of the section family does not
// use.
//
// §5.14 NOTE (recorded, not built here): 08-INIT-CONFIG-SPEC.md
// documents enabling enable_remote_runtime as an elevated verb. The only
// elevation-gated config surface that exists in this tree today is
// [elevation] itself (config_elevation.go: allow_remote, helper_pubkey),
// enforced by a hand-written "tightening only" check in Load. Extending
// that same enforcement to an arbitrary [plugins] key is a materially
// larger change than a single boolean config section, and this ticket's
// own task list asks only to "wire the config key" — so this section
// parses and defaults the value like every other boolean scalar in this
// file family, and the elevation gate on TOGGLING it is left an open gap,
// named here rather than silently assumed.
//
// Inputs: the decoded generic config tree (env overrides already applied).
// Outputs: a pluginsSection, or a typed *ConfigError for an unknown
// top-level key or a type mismatch.
// Constraints: enable_remote_runtime defaults to false (R-14.48: the
// remote plugin runtime is default-off; enabling it is itself the
// elevated verb, per the note above).
// SPORT: runtime/config-plugins (ADD, P1-E15-W4-S33-T4).

package runtime

// pluginsSection is the [plugins] table's typed view.
type pluginsSection struct {
	// EnableRemoteRuntime gates internal/plugins/remote.Dispatch: false
	// (the shipped default) makes `cascade plugin add` refuse a
	// remote-runtime manifest with a structured, non-fatal warning
	// instead of attempting a handshake; true lets the handshake run,
	// though every dispatch call still refuses with
	// remote.ErrRemoteRuntimeDeferred — full execution ships in P2.
	EnableRemoteRuntime bool
}

// defaultEnableRemoteRuntime is 08 §3's shipped default.
const defaultEnableRemoteRuntime = false

// pluginsTopKeys is the closed set of keys a [plugins] table recognises
// in the runtime config (distinct from the `cascade init` non-interactive
// [plugins] table's enable/disable/harness keys, which is a different
// document entirely — 08-INIT-CONFIG-SPEC.md §2 vs §3).
var pluginsTopKeys = map[string]bool{"enable_remote_runtime": true}

// parsePluginsSection type-checks tree's [plugins] table.
func parsePluginsSection(tree map[string]interface{}) (pluginsSection, error) {
	sec := pluginsSection{EnableRemoteRuntime: defaultEnableRemoteRuntime}
	raw, ok := tree["plugins"].(map[string]interface{})
	if !ok {
		return sec, nil
	}
	for k := range raw {
		if !pluginsTopKeys[k] {
			return pluginsSection{}, &ConfigError{Field: "plugins." + k, Reason: "unrecognised key in [plugins]"}
		}
	}
	v, ok := raw["enable_remote_runtime"]
	if !ok {
		return sec, nil
	}
	b, ok := v.(bool)
	if !ok {
		return pluginsSection{}, &ConfigError{Field: "plugins.enable_remote_runtime", Reason: "must be a boolean"}
	}
	sec.EnableRemoteRuntime = b
	return sec, nil
}
