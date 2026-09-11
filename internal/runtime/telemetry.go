package runtime

// Purpose: the [telemetry] config section stub (05 §Wave 6 §Epic AA
//   S-55.T3, §D-8). P1 ships exactly two things for telemetry: this typed
//   config surface (default off) and docs/TELEMETRY.md. No endpoint,
//   transport, collector, sampling, or emission code exists anywhere in
//   this tree; that half is EXPLICITLY DEFERRED to P2 (§D-8).
// Inputs: the decoded config tree (as parseLoggingSection/
//   parseElevationSection read it), the process environment, and the
//   warn sink Load's other section parsers use for unknown keys.
// Outputs: a TelemetryConfig with Enabled resolved per this ticket's
//   never-force-enable rule.
// Constraints: shipped default is enabled=false (08 §2: "opt-in only;
//   shipped default off"). CASCADE_TELEMETRY=0 forces enabled=false
//   regardless of the config file. No environment input may ever turn
//   telemetry on: the generic CASCADE_TELEMETRY__ENABLED override may
//   only narrow true->false, never widen false->true. Opt-in happens only
//   via an explicit [telemetry] enabled=true edit in config.toml, or the
//   init wizard's step-7 prompt (default No) writing that edit — never via
//   environment.
//
//   This ticket's files_scope adds this file only; it does not change
//   config.go, config_sections.go, or config_env.go, so the parse and
//   resolve entry points below are self-contained rather than wired into
//   Load's configSections/parseConfigSections chain. ResolveTelemetryConfig
//   is exported and real, but has no production caller yet — recorded in
//   internal/build/testonly-allow.json rather than left invisible, per the
//   TEST-ONLY gate. Wiring [telemetry] into Config/Load is a future
//   ticket's job (Epic C's config schema is CONSUMED here, never changed,
//   per 06 §5 rule 3 — no dep edge, no new cross-epic edge).
// SPORT: runtime/config-telemetry (ADD, placeholder per T-3 sport_updates).

// TelemetryConfig is the typed view of [telemetry] (08-INIT-CONFIG-SPEC
// §3, hot reload class: this section is not in coldSections, so a
// config.toml edit to it applies live under the same freezeColdSections
// path every other hot section uses). The shipped default is off.
type TelemetryConfig struct {
	// Enabled is telemetry.enabled. Default false. Opt-in only, via an
	// explicit config edit or the init wizard step-7 prompt (default No)
	// — never via environment (see ResolveTelemetryConfig).
	Enabled bool
}

// parseTelemetrySection type-checks tree's [telemetry] table, mirroring
// parseLoggingSection's shape: unset resolves to the default (Enabled
// false), an unrecognised key warns and is preserved rather than
// rejected, and a wrong-typed enabled is a typed ConfigError naming
// telemetry.enabled.
func parseTelemetrySection(tree map[string]interface{}, warn func(string, ...interface{})) (TelemetryConfig, error) {
	raw, _ := tree["telemetry"].(map[string]interface{})
	cfg := TelemetryConfig{Enabled: false}
	for k, v := range raw {
		switch k {
		case "enabled":
			b, ok := v.(bool)
			if !ok {
				return TelemetryConfig{}, &ConfigError{Field: "telemetry.enabled", Reason: "must be a boolean"}
			}
			cfg.Enabled = b
		default:
			warn("runtime: unknown key telemetry.%s in config.toml (preserved, not validated)", k)
		}
	}
	return cfg, nil
}

// ResolveTelemetryConfig computes [telemetry]'s effective configuration
// from tree (the config.toml tree exactly as readAndUpgradeTree decodes
// it, BEFORE Load's generic CASCADE_<SECTION>__<KEY> merge — this
// function applies environment rules itself, rather than relying on
// Load's merge, precisely because those generic rules are not safe to
// apply unmodified to telemetry) and environ/getenv, enforcing the
// never-force-enable rule (08 §2):
//
//   - The base value comes from tree's [telemetry] table alone (via
//     parseTelemetrySection); an invalid value is a typed ConfigError.
//   - The generic CASCADE_TELEMETRY__ENABLED override (collected the same
//     way Load's own collectEnvOverrides does, reused unchanged from
//     config_env.go) may narrow true->false, but a true value from that
//     override is refused: it never widens false->true.
//   - CASCADE_TELEMETRY=0 (08 §2's named var) forces Enabled=false
//     regardless of the file or the generic override. No defined value of
//     CASCADE_TELEMETRY ever forces enabled=true; the var has no
//     enable-side effect at all.
//
// A nil environ or getenv is treated as "no environment input" (matching
// Load's loadOptionDefaults resolving both to os.Environ/os.Getenv when a
// caller supplies neither), so production use with real accessors and
// test use with fakes both flow through the same three steps.
func ResolveTelemetryConfig(tree map[string]interface{}, environ func() []string, getenv Getenv, warn func(string, ...interface{})) (TelemetryConfig, error) {
	cfg, err := parseTelemetrySection(tree, warn)
	if err != nil {
		return TelemetryConfig{}, err
	}

	if environ != nil {
		if raw, ok := collectEnvOverrides(environ())["telemetry.enabled"]; ok {
			if b, ok := raw.(bool); ok && !b {
				cfg.Enabled = false
			}
			// raw == true (or a non-bool literal) is a widen attempt and
			// is silently refused: cfg.Enabled is left exactly as the
			// file/default resolved it, never forced to true here.
		}
	}

	if getenv != nil && getenv("CASCADE_TELEMETRY") == "0" {
		cfg.Enabled = false
	}

	return cfg, nil
}
