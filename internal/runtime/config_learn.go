package runtime

// Purpose: the [learn.retention] table parser -- max_age_days (int,
//   default 90, hot reload class) for P1-E31-W6-S64-T1's telemetry
//   retention sweep.
//
// DEVIATION (declared, build-lane-rules §SCOPE): this ticket's files_scope
// lists only internal/runtime/config.go under "change". Every existing
// single-key section in this package (config_ci_watch.go,
// config_economics.go, config_registry.go, ...) requires touching THREE
// files -- its own sibling file, config.go's Config struct literal, and
// config_sections.go's ONE registration point (R-21.267, and
// config_sections.go's own header: "purely so each stays readable" under
// Art.10.3's 300-line cap) -- this ticket follows that same established,
// load-bearing precedent rather than inlining a fourth typed section into
// an already-282-line config.go.
//
// Inputs: the decoded generic config tree.
// Outputs: a learnSection, or a typed *ConfigError for a non-table
//   [learn.retention] or a non-positive max_age_days.
// Constraints: hot reload class -- "learn" is not in hotreload.go's
//   coldSections, so a config.toml edit applies at the daemon's NEXT
//   RetentionSweep.Run (retention.go re-reads per run), no cold-key diff
//   gate involved.
// SPORT: internal.runtime.parseLearnSection/ADDED (P1-E31-W6-S64-T1).

// learnRetentionSection is [learn.retention].
type learnRetentionSection struct {
	// MaxAgeDays is telemetry retention's cutoff, in days. 0 means unset --
	// internal/learn.RetentionMaxAgeDays falls back to
	// learn.DefaultMaxAgeDays (90); this package never invents that
	// numeric default itself (R-14.107).
	MaxAgeDays int
}

// learnSection is [learn].
type learnSection struct {
	Retention learnRetentionSection
}

// parseLearnSection type-checks tree's [learn] table. An absent [learn]
// or [learn.retention] table parses to the zero value.
func parseLearnSection(tree map[string]interface{}) (learnSection, error) {
	learnTree, ok := tree["learn"].(map[string]interface{})
	if !ok {
		return learnSection{}, nil
	}
	retRaw, present := learnTree["retention"]
	if !present {
		return learnSection{}, nil
	}
	retTree, ok := retRaw.(map[string]interface{})
	if !ok {
		return learnSection{}, &ConfigError{Field: "learn.retention", Reason: "must be a table"}
	}
	var sec learnSection
	for k, v := range retTree {
		if k != "max_age_days" {
			return learnSection{}, &ConfigError{Field: "learn.retention." + k, Reason: "unrecognised key in [learn.retention]"}
		}
		n, err := tomlInt(v)
		if err != nil || n <= 0 {
			return learnSection{}, &ConfigError{Field: "learn.retention.max_age_days", Reason: "must be a positive integer"}
		}
		sec.Retention.MaxAgeDays = n
	}
	return sec, nil
}
