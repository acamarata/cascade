package runtime

import "fmt"

// Purpose: the [logging] table parser behind Load (config.go):
//   shape-validates level/format/rotation and reads the two rotation keys
//   as *int so R-14.107's "no numeric default" ruling has a concrete
//   representation (nil = unset). Split out as its own sibling file per
//   R-14.117 (Art.10.3 file-cap remedy — config.go was already close to
//   the 300-line cap before this ticket's typed [logging] read landed);
//   this mirrors config_load.go's parseElevationSection/
//   resolveRuntimeSection pattern for the section this ticket owns.
// Inputs: the decoded generic config tree (env overrides already
//   applied, same as parseElevationSection's input) and an injected warn
//   sink for unrecognised top-level [logging] keys.
// Outputs: a loggingSection, or a typed *ConfigError naming the
//   offending field for a type mismatch, an invalid level/format value,
//   or an unrecognised [logging.rotation] key.
// Constraints: R-14.107 — logging.rotation.max_size_mb and .max_files
//   never receive an invented default; an unset key leaves the
//   corresponding loggingRotation pointer nil, which loggingRotation.
//   Enabled() (config.go) reads as "rotation disabled". [logging] itself
//   tolerates unknown top-level keys (warn, matching [runtime] — 08
//   leaves room for later additive keys); [logging.rotation] does not
//   (hard error), matching [elevation]'s small-closed-set strictness.
// SPORT: runtime/logger (ADD, per T-2 sport_updates).

// parseLoggingSection type-checks tree's [logging] table (08 §3): level
// and format resolve to "info"/"json" when unset.
func parseLoggingSection(tree map[string]interface{}, warn func(string, ...interface{})) (loggingSection, error) {
	raw, _ := tree["logging"].(map[string]interface{})
	logging := loggingSection{Level: "info", Format: "json"}
	for k, v := range raw {
		switch k {
		case "level":
			s, ok := v.(string)
			if !ok {
				return loggingSection{}, &ConfigError{Field: "logging.level", Reason: "must be a string"}
			}
			if s != "debug" && s != "info" && s != "warn" && s != "error" {
				return loggingSection{}, &ConfigError{Field: "logging.level", Reason: "must be one of debug, info, warn, error"}
			}
			logging.Level = s
		case "format":
			s, ok := v.(string)
			if !ok {
				return loggingSection{}, &ConfigError{Field: "logging.format", Reason: "must be a string"}
			}
			if s != "json" && s != "text" {
				return loggingSection{}, &ConfigError{Field: "logging.format", Reason: "must be json or text"}
			}
			logging.Format = s
		case "rotation":
			rot, err := parseLoggingRotation(v)
			if err != nil {
				return loggingSection{}, err
			}
			logging.Rotation = rot
		default:
			warn("runtime: unknown key logging.%s in config.toml (preserved, not validated)", k)
		}
	}
	return logging, nil
}

// parseLoggingRotation type-checks [logging.rotation]. Both keys are
// optional; R-14.107 forbids defaulting either, so an absent key leaves
// the corresponding loggingRotation field nil. Unlike [logging]'s
// top-level tolerance, an unrecognised key here is a hard typed error —
// this table is small and closed, so a typo (e.g. max_size for
// max_size_mb) should never be silently ignored.
func parseLoggingRotation(v interface{}) (loggingRotation, error) {
	raw, ok := v.(map[string]interface{})
	if !ok {
		return loggingRotation{}, &ConfigError{Field: "logging.rotation", Reason: "must be a table"}
	}
	rot := loggingRotation{}
	for k, val := range raw {
		switch k {
		case "max_size_mb", "max_files":
			n, err := tomlInt(val)
			if err != nil || n <= 0 {
				return loggingRotation{}, &ConfigError{Field: "logging.rotation." + k, Reason: "must be a positive integer"}
			}
			if k == "max_size_mb" {
				rot.MaxSizeMB = &n
			} else {
				rot.MaxFiles = &n
			}
		default:
			return loggingRotation{}, &ConfigError{Field: "logging.rotation." + k, Reason: "unrecognised key in [logging.rotation]"}
		}
	}
	return rot, nil
}

// tomlInt coerces a decoded TOML numeric leaf to int, tolerating the
// several shapes a real decode (int64) or a hand-built test tree (int,
// float64) may produce — mirrors schemaVersionOf's tolerance in
// schema.go.
func tomlInt(v interface{}) (int, error) {
	switch n := v.(type) {
	case int64:
		return int(n), nil
	case int:
		return n, nil
	case float64:
		return int(n), nil
	default:
		return 0, fmt.Errorf("not a number")
	}
}
