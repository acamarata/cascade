// Package policy (autonomy_config.go): Purpose: the [policy] config
//
//	section binding — parsing, validating and resolving the complete
//	08-INIT-CONFIG-SPEC §3 key set (autonomy_profile, the allow/ask/deny
//	overlay lists, approval_batch_window_s and approval_batch_cap, R-14.29)
//	into an immutable AutonomyProfile plus the approval batching numerics
//	S-18.T3 reads through this API.
//
// Inputs: the decoded generic config tree (the same map[string]interface{}
//
//	internal/runtime hands every section parser), or nothing at all.
//
// Outputs: Config, ApprovalBatching, a resolved *AutonomyProfile, or
//
//	a typed refusal.
//
// Constraints: FAIL CLOSED and TIGHTENING-ONLY. Every malformed input is a
//
//	hard error rather than a defaulted value, because the failure this
//	subsystem exists to prevent is a config that looks accepted while
//	quietly selecting a more permissive table than the operator wrote: an
//	unknown profile name, a misspelled key inside [policy], a level name
//	that is not a rung, a level named in two overlay lists, and an overlay
//	that would WIDEN its profile's slot are all refusals. The caller keeps
//	its running profile on a refusal (autonomy_controller.go), so a bad
//	edit never relaxes a running system.
//
// SPORT: internal/policy Config/ADDED, ApprovalBatching/ADDED
//
//	(P1-E09-W2-S18-T1).
package policy

import (
	"fmt"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Default approval-batching numerics (08 §3, R-14.29). They apply when the
// key is absent; an explicitly written out-of-range value is refused
// rather than clamped to these.
const (
	// DefaultApprovalBatchWindowSeconds is [policy].approval_batch_window_s.
	DefaultApprovalBatchWindowSeconds = 10
	// DefaultApprovalBatchCap is [policy].approval_batch_cap.
	DefaultApprovalBatchCap = 20
)

// Bounds on the two approval numerics. A window of zero would batch
// nothing and a cap of zero would batch everything into a queue that never
// drains, so neither is a usable value; the upper bounds keep a typo
// (a pasted timestamp, say) from becoming an hours-long approval window.
const (
	maxApprovalBatchWindowSeconds = 3600
	maxApprovalBatchCap           = 1000
)

// policySectionKey is the [policy] table's name in the config tree.
const policySectionKey = "policy"

// policyForeignKeys are keys that legitimately live under [policy] but
// belong to another ticket. They are neither parsed nor defaulted here —
// they are simply not misspellings, so they do not trip the strict
// unknown-key refusal below.
var policyForeignKeys = map[string]bool{
	// preset is the init wizard's policy-preset shorthand (R-16.34,
	// AM/S-76.T1).
	"preset": true,
	// risk_gates is [policy.risk_gates], the tightening-only per-class
	// gate table (R-16.70, AH/S-69.T1).
	"risk_gates": true,
}

// ApprovalBatching holds the two approval-queue numerics R-14.29 ratified.
// S-18.T3 reads them through this type and binds no config keys of its own.
type ApprovalBatching struct {
	// WindowSeconds is how long the queue accumulates approvals before
	// presenting them as one batch.
	WindowSeconds int `json:"window_seconds"`
	// Cap is the largest number of approvals one batch may hold.
	Cap int `json:"cap"`
}

// defaultApprovalBatching returns the 08 §3 defaults.
func defaultApprovalBatching() ApprovalBatching {
	return ApprovalBatching{
		WindowSeconds: DefaultApprovalBatchWindowSeconds,
		Cap:           DefaultApprovalBatchCap,
	}
}

// Config is the parsed, not-yet-resolved [policy] section: exactly
// what the file said, with defaults filled in for absent keys and nothing
// else interpreted.
type Config struct {
	// ProfileName is [policy].autonomy_profile. Empty means the key was
	// absent, which resolves to the §5.15 baseline; an explicitly empty
	// string is a refusal, not an absence.
	ProfileName string `json:"autonomy_profile"`
	// Overlays holds the levels named by the allow/ask/deny lists, keyed
	// by the verdict the list assigns.
	Overlays map[Verdict][]RiskLevel `json:"-"`
	// Batching holds the two approval numerics.
	Batching ApprovalBatching `json:"batching"`
}

// ParseConfig reads the [policy] section out of tree.
//
// An absent tree or an absent [policy] section is not an error: it yields
// the §5.15 baseline profile and the 08 §3 batching defaults, which is the
// documented default state of an unconfigured install. Absence is the only
// thing treated leniently here — everything present is parsed strictly.
func ParseConfig(tree map[string]interface{}) (Config, error) {
	out := Config{Overlays: map[Verdict][]RiskLevel{}, Batching: defaultApprovalBatching()}
	raw, ok := tree[policySectionKey]
	if !ok {
		return out, nil
	}
	table, ok := raw.(map[string]interface{})
	if !ok {
		return Config{}, newConfigError("policy", "must be a table")
	}
	if err := checkPolicyKeys(table); err != nil {
		return Config{}, err
	}
	if err := out.parseProfileName(table); err != nil {
		return Config{}, err
	}
	if err := out.parseOverlays(table); err != nil {
		return Config{}, err
	}
	if err := out.parseBatching(table); err != nil {
		return Config{}, err
	}
	return out, nil
}

// policyOwnedKeys are the keys this ticket parses: the complete 08 §3
// [policy] set (autonomy_profile, the three overlay lists, and the two
// approval numerics R-14.29 added).
var policyOwnedKeys = map[string]bool{
	"autonomy_profile":        true,
	"allow":                   true,
	"ask":                     true,
	"deny":                    true,
	"approval_batch_window_s": true,
	"approval_batch_cap":      true,
}

// checkPolicyKeys refuses any key under [policy] that is neither owned
// here nor another ticket's known key.
//
// This is strict on purpose. The permissive alternative — warn and
// continue — means `autonomy_profil = "strict"` loads successfully with
// the profile still at the baseline, i.e. a typo silently choosing the
// more permissive of the two tables. A refusal that keeps the running
// profile is the safe reading of an operator's ambiguous edit.
func checkPolicyKeys(table map[string]interface{}) error {
	unknown := make([]string, 0, len(table))
	for k := range table {
		if policyOwnedKeys[k] || policyForeignKeys[k] {
			continue
		}
		unknown = append(unknown, sanitize(k))
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return newConfigError("policy",
		"unrecognised key(s): %s", strings.Join(unknown, ", "))
}

// parseProfileName reads autonomy_profile. A present key must be a
// non-empty string naming a built-in profile; the name check itself
// happens in Resolve, so a caller that only parses still sees a
// well-formed value.
func (c *Config) parseProfileName(table map[string]interface{}) error {
	raw, ok := table["autonomy_profile"]
	if !ok {
		return nil
	}
	name, ok := raw.(string)
	if !ok {
		return newConfigError("policy.autonomy_profile", "must be a string")
	}
	if strings.TrimSpace(name) == "" {
		return newConfigError("policy.autonomy_profile", "is empty")
	}
	c.ProfileName = strings.TrimSpace(name)
	return nil
}

// parseBatching reads the two approval numerics, with the 08 §3 defaults
// for absent keys and a hard refusal for an out-of-range present one.
func (c *Config) parseBatching(table map[string]interface{}) error {
	window, err := parseBoundedInt(table, "approval_batch_window_s",
		DefaultApprovalBatchWindowSeconds, maxApprovalBatchWindowSeconds)
	if err != nil {
		return err
	}
	batchCap, err := parseBoundedInt(table, "approval_batch_cap",
		DefaultApprovalBatchCap, maxApprovalBatchCap)
	if err != nil {
		return err
	}
	c.Batching = ApprovalBatching{WindowSeconds: window, Cap: batchCap}
	return nil
}

// parseBoundedInt reads one integer key, returning def when it is absent
// and refusing anything that is not a whole number in [1, max]. TOML
// decodes integers as int64, and a float or a string here is a malformed
// value rather than a number to coerce.
func parseBoundedInt(table map[string]interface{}, key string, def, upper int) (int, error) {
	raw, ok := table[key]
	if !ok {
		return def, nil
	}
	var n int64
	switch v := raw.(type) {
	case int64:
		n = v
	case int:
		n = int64(v)
	default:
		return 0, newConfigError("policy."+key, "must be an integer")
	}
	if n < 1 || n > int64(upper) {
		return 0, newConfigError("policy."+key,
			"must be between 1 and %d", upper)
	}
	return int(n), nil
}

// newConfigError builds the typed refusal every path in this file
// returns. KindInvalidInput is the taxonomy's entry for a malformed input,
// and the field name is dotted exactly as `cascade config get` spells it.
func newConfigError(field, format string, args ...interface{}) error {
	reason := format
	if len(args) > 0 {
		reason = fmt.Sprintf(format, args...)
	}
	return cascade.Newf(cascade.KindInvalidInput, "policy: config: %s: %s", field, reason)
}
