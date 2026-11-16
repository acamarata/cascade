// Purpose: the `[ci.policy]`, `[ci.local]` and `[ci].mirror_remote` parsers
// behind Load, a sibling file so config.go stays under its 300-line cap.
// Inputs: the decoded generic config tree (env overrides already applied).
// Outputs: typed sections, or a *ConfigError for an unrecognised key or a
// malformed value.
// Constraints: fail-closed on a malformed "owner/repo" pattern or a blank
// command string; never silently drop a bad entry.
// SPORT: internal.runtime.parseCIPolicySection/ADDED,
//
//	internal.runtime.parseCILocalSection/ADDED (P1-E25-W5-S51-T5),
//	internal.runtime.parseCIAffectedCmdField/ADDED (P1-E32-W6-S65-T1,
//	fix round 2, AMD-20260922/F7-7, R-21.173),
//	internal.runtime.parseCIToolchainVersions/ADDED (P1-CI-03).

package runtime

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
)

// ciPolicySection is the `[ci.policy]` table's typed view: a list of
// "owner/repo" patterns naming private repositories. There is deliberately
// no `allow_paid` or similar bypass field.
type ciPolicySection struct {
	// PrivateRepos are "owner/repo" patterns; a repo matching one of
	// these routes to the local gate rather than GitHub Actions.
	PrivateRepos []string
}

// ciLocalSection is the `[ci.local]` table's typed view. Lint/Test/Build are
// nil when unset; internal/ci decides the defaults.
type ciLocalSection struct {
	Lint  []string
	Test  []string
	Build []string
	// EnvKeys are extra environment-variable NAMES passed to every step on
	// top of internal/ci's fixed allowlist. Names, never values.
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

// parsePrivateRepoPatterns type-checks [ci.policy.repos]'s "private" array.
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

// validateRepoPattern checks one [ci.policy.repos.private] entry at load
// time. Entries are path.Match globs over "owner/repo"; the resolver in
// plugins/github/cipolicy uses the same call at match time.
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

// ciCommandList type-checks one [ci.local] command-list key: non-blank
// command strings, or absent (nil).
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

// ciEnvKeys type-checks [ci.local] env: variable NAMES only; a blank name or
// one carrying "=" is rejected, since this key never sets a value.
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

// parseCIAffectedCmdField type-checks the top-level `[ci].affected_cmd`
// string. Absent or blank means "not configured" (internal/ci then falls
// back to TargetAll). A present value is kept verbatim; only
// internal/ci/affected_cmd.go passes it to a shell.
func parseCIAffectedCmdField(tree map[string]interface{}) (string, error) {
	ciTree, ok := tree["ci"].(map[string]interface{})
	if !ok {
		return "", nil
	}
	raw, present := ciTree["affected_cmd"]
	if !present {
		return "", nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", &ConfigError{Field: "ci.affected_cmd", Reason: "must be a string"}
	}
	return s, nil
}

// parseCIToolchainVersions type-checks [ci].toolchain_versions (P1-CI-03): non-blank strings, absent = empty; hot.
func parseCIToolchainVersions(tree map[string]interface{}) ([]string, error) {
	ciTree, _ := tree["ci"].(map[string]interface{})
	raw, present := ciTree["toolchain_versions"]
	list, ok := raw.([]interface{})
	out := make([]string, 0, len(list))
	for i := 0; ok && i < len(list); i++ {
		if v, isStr := list[i].(string); isStr && strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		} else {
			return nil, &ConfigError{Field: fmt.Sprintf("ci.toolchain_versions[%d]", i), Reason: "must be a non-empty version string"}
		}
	}
	if present && !ok {
		return nil, &ConfigError{Field: "ci.toolchain_versions", Reason: "must be an array of version strings"}
	}
	return out, nil
}

// nestedTable reads tree[a][b] as a map; either level absent is ok=false.
func nestedTable(tree map[string]interface{}, a, b string) (map[string]interface{}, bool) {
	outer, ok := tree[a].(map[string]interface{})
	if !ok {
		return nil, false
	}
	inner, ok := outer[b].(map[string]interface{})
	return inner, ok
}

// CIMirrorRemote is [ci].mirror_remote, the one remote an accepted branch is
// mirrored to; empty (default) is off. It must be a URL or user@host:path
// address with no password or token.
func (c *Config) CIMirrorRemote() (string, error) {
	ciTree, _ := c.rawTree["ci"].(map[string]interface{})
	raw, present := ciTree["mirror_remote"]
	s, ok := raw.(string)
	if present && !ok {
		return "", &ConfigError{Field: "ci.mirror_remote", Reason: "must be a string"}
	}
	if s = strings.TrimSpace(s); s == "" {
		return "", nil
	}
	if _, err := MirrorRemoteHost(s); err != nil {
		return "", &ConfigError{Field: "ci.mirror_remote", Reason: err.Error()}
	}
	return s, nil
}

// MirrorRemoteHost returns the host of a mirror remote address and refuses
// one with a leading dash, whitespace, an embedded password or no host.
func MirrorRemoteHost(remote string) (string, error) {
	if remote == "" || strings.HasPrefix(remote, "-") || strings.ContainsAny(remote, " \t\r\n") {
		return "", errors.New("must be a git URL or user@host:path address")
	}
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil || u.Hostname() == "" {
			return "", errors.New("must name a host")
		}
		if _, hasPw := u.User.Password(); hasPw {
			return "", errors.New("must not embed a password or token; use the git credential helper")
		}
		return strings.ToLower(u.Hostname()), nil
	}
	host, _, found := strings.Cut(remote[strings.LastIndex(remote, "@")+1:], ":")
	if !found || host == "" || strings.Contains(host, "/") {
		return "", errors.New("must be a git URL or user@host:path address")
	}
	return strings.ToLower(host), nil
}
