// Purpose: the `[ci.policy]` and `[ci.local]` table parsers behind Load
// (config.go), per P1-E25-W5-S51-T5's task 1 (08-INIT-CONFIG-SPEC.md §3
// gains these two rows, R-21.267) — split out as its own sibling file
// following config_widget.go's/config_economics.go's established remedy
// (R-14.117) rather than growing config.go past its 300-line cap.
//
// FILES_SCOPE DEVIATION (recorded, same reasoning as config_fleet.go's
// and config_economics.go's own identical notes). This ticket's contract
// names only "internal/runtime/config/schema.go" (resolved, per this
// ticket's own execution_guidance, to internal/runtime/config.go) as the
// registration point. Every existing section in this package (widget,
// economics, fleet accounts, plugins, retrieval) is its own sibling file,
// not inline in config.go — that IS the registration convention R-21.267
// itself describes ("the only config registration point" means Load/
// Config/EffectiveEntries wire every section, not that every section's
// parser lives in one physical file, which Art.10.3's 300-line cap would
// make impossible). This file and its wiring into config_sections.go and
// config.go follow that same established shape.
//
// Inputs: the decoded generic config tree (env overrides already applied).
// Outputs: a ciPolicySection / ciLocalSection, or a typed *ConfigError for
// an unrecognised key or a malformed value.
// Constraints: fail-closed on a malformed "owner/repo" pattern or a blank
// command string (06 §5.20) — never silently drop a bad entry. No numeric
// default is invented for timeout_seconds beyond the one this file itself
// documents (defaultCITimeoutSeconds), matching loggingRotation's R-14.107
// discipline of naming every default explicitly.
// SPORT: internal.runtime.parseCIPolicySection/ADDED,
//
//	internal.runtime.parseCILocalSection/ADDED (P1-E25-W5-S51-T5).

package runtime

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// ciPolicySection is the `[ci.policy]` table's typed view: the
// never-pay routing policy's only configurable input, a list of
// "owner/repo" patterns naming private repositories (full_desc's
// `[ci.policy.repos.private]`). There is deliberately no `allow_paid`
// or similar bypass field — R-14.77 is enforced by this type's shape,
// not by a flag a caller could set to defeat it.
type ciPolicySection struct {
	// PrivateRepos are "owner/repo" patterns; a repo matching one of
	// these routes to the local gate rather than GitHub Actions.
	PrivateRepos []string
}

// ciLocalSection is the `[ci.local]` table's typed view: the local CI
// runner's step commands and per-step timeout. Lint/Test/Build are nil
// when unset in config.toml -- internal/ci's own runner_config.go decides
// the repo-type-derived default commands; this file only type-checks
// what config.toml actually declares.
type ciLocalSection struct {
	Lint  []string
	Test  []string
	Build []string
	// EnvKeys are the extra environment-variable NAMES the operator wants
	// passed through to every step, on top of internal/ci's fixed
	// allowlist. Names, never values: a step's environment is BUILT from an
	// allowlist rather than inherited, so this key widens that list; it
	// never introduces a secret of its own into config.toml.
	EnvKeys        []string
	TimeoutSeconds int
}

// defaultCITimeoutSeconds is [ci.local]'s shipped default for
// timeout_seconds when the key is absent.
const defaultCITimeoutSeconds = 300

var ciLocalTopKeys = map[string]bool{
	"lint": true, "test": true, "build": true, "env": true, "timeout_seconds": true,
}

// parseCIPolicySection type-checks tree's [ci.policy] table.
func parseCIPolicySection(tree map[string]interface{}) (ciPolicySection, error) {
	policyTree, ok := nestedTable(tree, "ci", "policy")
	if !ok {
		return ciPolicySection{}, nil
	}
	for k := range policyTree {
		if k != "repos" {
			return ciPolicySection{}, &ConfigError{Field: "ci.policy." + k, Reason: "unrecognised key in [ci.policy]"}
		}
	}
	reposTree, ok := policyTree["repos"].(map[string]interface{})
	if !ok {
		if _, present := policyTree["repos"]; present {
			return ciPolicySection{}, &ConfigError{Field: "ci.policy.repos", Reason: "must be a table"}
		}
		return ciPolicySection{}, nil
	}
	patterns, err := parsePrivateRepoPatterns(reposTree)
	if err != nil {
		return ciPolicySection{}, err
	}
	return ciPolicySection{PrivateRepos: patterns}, nil
}

// parsePrivateRepoPatterns type-checks [ci.policy.repos]'s "private" array,
// factored out of parseCIPolicySection to keep it under Art.10.3's 50-line
// funlen cap.
func parsePrivateRepoPatterns(reposTree map[string]interface{}) ([]string, error) {
	for k := range reposTree {
		if k != "private" {
			return nil, &ConfigError{Field: "ci.policy.repos." + k, Reason: "unrecognised key in [ci.policy.repos]"}
		}
	}
	raw, ok := reposTree["private"]
	if !ok {
		return nil, nil
	}
	list, ok := raw.([]interface{})
	if !ok {
		return nil, &ConfigError{Field: "ci.policy.repos.private", Reason: "must be an array of \"owner/repo\" strings"}
	}
	patterns := make([]string, 0, len(list))
	for i, v := range list {
		s, ok := v.(string)
		if !ok {
			return nil, &ConfigError{
				Field:  fmt.Sprintf("ci.policy.repos.private[%d]", i),
				Reason: "must be a non-empty \"owner/repo\" pattern",
			}
		}
		if err := validateRepoPattern(s); err != nil {
			return nil, &ConfigError{
				Field:  fmt.Sprintf("ci.policy.repos.private[%d]", i),
				Reason: err.Error(),
			}
		}
		patterns = append(patterns, s)
	}
	return patterns, nil
}

// validateRepoPattern checks one [ci.policy.repos.private] entry at LOAD
// time, so a typo is a startup error naming the key rather than a silent
// routing hole later.
//
// The entries are GLOB patterns over "owner/repo" (path.Match semantics),
// not exact strings: "acamarata/*" is meant to cover every repository
// under one owner. path.Match reports ErrBadPattern from the pattern
// alone, so the probe subject below is arbitrary. The same semantics are
// applied at match time by the resolver in plugins/github/cipolicy --
// deliberately the same standard-library call on both sides rather than a
// second hand-written matcher that could disagree with this check.
func validateRepoPattern(pattern string) error {
	trimmed := strings.TrimSpace(pattern)
	if trimmed == "" || !strings.Contains(trimmed, "/") {
		return errors.New("must be a non-empty \"owner/repo\" pattern")
	}
	if _, err := path.Match(trimmed, "owner/repo"); err != nil {
		return errors.New("is not a valid owner/repo glob pattern: " + err.Error())
	}
	return nil
}

// parseCILocalSection type-checks tree's [ci.local] table.
func parseCILocalSection(tree map[string]interface{}) (ciLocalSection, error) {
	sec := ciLocalSection{TimeoutSeconds: defaultCITimeoutSeconds}
	localTree, ok := nestedTable(tree, "ci", "local")
	if !ok {
		return sec, nil
	}
	for k := range localTree {
		if !ciLocalTopKeys[k] {
			return ciLocalSection{}, &ConfigError{Field: "ci.local." + k, Reason: "unrecognised key in [ci.local]"}
		}
	}
	var err error
	if sec.Lint, err = ciCommandList(localTree, "lint"); err != nil {
		return ciLocalSection{}, err
	}
	if sec.Test, err = ciCommandList(localTree, "test"); err != nil {
		return ciLocalSection{}, err
	}
	if sec.Build, err = ciCommandList(localTree, "build"); err != nil {
		return ciLocalSection{}, err
	}
	if sec.EnvKeys, err = ciEnvKeys(localTree); err != nil {
		return ciLocalSection{}, err
	}
	if v, present := localTree["timeout_seconds"]; present {
		n, err := tomlInt(v)
		if err != nil || n <= 0 {
			return ciLocalSection{}, &ConfigError{Field: "ci.local.timeout_seconds", Reason: "must be a positive integer"}
		}
		sec.TimeoutSeconds = n
	}
	return sec, nil
}

// ciCommandList type-checks one [ci.local] command-list key (lint/test/
// build): an array of non-blank command strings, or absent (nil, "use the
// repo-type default").
func ciCommandList(localTree map[string]interface{}, key string) ([]string, error) {
	raw, present := localTree[key]
	if !present {
		return nil, nil
	}
	list, ok := raw.([]interface{})
	if !ok {
		return nil, &ConfigError{Field: "ci.local." + key, Reason: "must be an array of command strings"}
	}
	cmds := make([]string, 0, len(list))
	for i, v := range list {
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return nil, &ConfigError{Field: fmt.Sprintf("ci.local.%s[%d]", key, i), Reason: "must be a non-empty command string"}
		}
		cmds = append(cmds, s)
	}
	return cmds, nil
}

// ciEnvKeys type-checks [ci.local] env: an array of environment-variable
// NAMES to pass through to every step. A name is rejected if it is blank
// or carries an "=" -- that would be a NAME=VALUE pair, and this key
// deliberately cannot set a value, only widen the pass-through allowlist.
func ciEnvKeys(localTree map[string]interface{}) ([]string, error) {
	raw, present := localTree["env"]
	if !present {
		return nil, nil
	}
	list, ok := raw.([]interface{})
	if !ok {
		return nil, &ConfigError{Field: "ci.local.env", Reason: "must be an array of environment-variable names"}
	}
	keys := make([]string, 0, len(list))
	for i, v := range list {
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" || strings.Contains(s, "=") {
			return nil, &ConfigError{
				Field:  fmt.Sprintf("ci.local.env[%d]", i),
				Reason: "must be a non-empty variable NAME (no \"=\": this key widens the pass-through allowlist, it never sets a value)",
			}
		}
		keys = append(keys, strings.TrimSpace(s))
	}
	return keys, nil
}

// nestedTable reads tree[a][b] as a map, tolerating either level being
// absent (ok=false, never an error -- an absent table means "use every
// default", exactly like widgetSection's own top-level absence handling).
func nestedTable(tree map[string]interface{}, a, b string) (map[string]interface{}, bool) {
	outer, ok := tree[a].(map[string]interface{})
	if !ok {
		return nil, false
	}
	inner, ok := outer[b].(map[string]interface{})
	return inner, ok
}
