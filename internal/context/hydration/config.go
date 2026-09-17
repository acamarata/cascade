// Package hydration holds the prompt-hydration feature's shared pieces:
// its configuration section, the degraded signal it publishes when it
// silently does nothing, and the doctor check that counts them.
//
// Purpose: the [context.hydration] configuration section (08-INIT-CONFIG-
//
//	SPEC §3, R-16.6a) — the four constants the prompt-hydration hook reads
//	and the parser that resolves them from a decoded config document.
//
// Inputs: runtime.Config.Extra["context"]["hydration"], or nil.
// Outputs: a Config with every field resolved, or a typed refusal.
// Constraints: an ABSENT section is valid and yields the defaults —
//
//	hydration is on by default (R-16.6a). A PRESENT but malformed section
//	is a typed error, never a silent fall back to the defaults: a user who
//	wrote `enabled = "no"` meant something by it, and quietly enabling
//	hydration because the value did not parse is the opposite of what they
//	asked for.
//
// SPORT: internal/context/hydration (ADD) — P1-E16-W4-S34-T4.
package hydration

import (
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// SectionName is this section's key under the [context] table.
const SectionName = "hydration"

// ParentSectionName is the top-level table SectionName lives under.
const ParentSectionName = "context"

// The R-16.6a constants. They are named rather than inlined because the
// hook, the doctor check and the tests all have to agree about them, and a
// literal typed in three places is a constant that drifts.
const (
	// DefaultEnabled is hydration's default state. On: a harness session
	// that starts blind to the context engine is the problem this hook
	// exists to fix, so the fix is not opt-in.
	DefaultEnabled = true
	// DefaultBudgetTokens is the slice budget, passed through as
	// --budget. The E/S-09.T1 budgeter does the counting; this ticket
	// adds no second counter.
	DefaultBudgetTokens = 2000
	// DefaultMinScore filters the F/S-11.T1 normalized fused score. A
	// record below it is dropped: injecting weak matches into every
	// prompt costs budget and attention for context the model did not
	// need.
	DefaultMinScore = 0.35
	// DefaultTimeout bounds the whole hook. On expiry the hook injects
	// NOTHING and exits 0 — hydration is context, not policy, so it
	// fails OPEN and the session proceeds unhydrated.
	DefaultTimeout = 3 * time.Second
)

// Config is the resolved section.
type Config struct {
	// Enabled gates INSTALLATION of the hook, not per-invocation
	// behaviour. Scope safety is the scope resolver's job, never this
	// switch's (R-16.6a).
	Enabled bool
	// BudgetTokens is the --budget passed to the slice.
	BudgetTokens int
	// MinScore is the fused-score floor a retrieved record must clear.
	MinScore float64
	// Timeout bounds the hook.
	Timeout time.Duration
}

// Default returns the R-16.6a defaults.
func Default() Config {
	return Config{
		Enabled:      DefaultEnabled,
		BudgetTokens: DefaultBudgetTokens,
		MinScore:     DefaultMinScore,
		Timeout:      DefaultTimeout,
	}
}

// LoadSection resolves the section from a decoded [context] table.
//
// parent is runtime.Config.Extra["context"], or nil. Both an absent
// [context] table and an absent [context.hydration] sub-table yield the
// defaults; anything present but not a table, or any field present with
// the wrong type or an out-of-range value, is a typed refusal.
func LoadSection(parent interface{}) (Config, error) {
	cfg := Default()
	if parent == nil {
		return cfg, nil
	}
	contextTable, ok := parent.(map[string]interface{})
	if !ok {
		return Config{}, cascade.Newf(cascade.KindInvalidInput,
			"config: [%s] is %T, want a table", ParentSectionName, parent)
	}
	raw, present := contextTable[SectionName]
	if !present || raw == nil {
		return cfg, nil
	}
	table, ok := raw.(map[string]interface{})
	if !ok {
		return Config{}, cascade.Newf(cascade.KindInvalidInput,
			"config: [%s.%s] is %T, want a table", ParentSectionName, SectionName, raw)
	}
	return parseTable(table, cfg)
}

// parseTable reads each known key, leaving an absent key at its default.
func parseTable(table map[string]interface{}, cfg Config) (Config, error) {
	if err := readBool(table, "enabled", &cfg.Enabled); err != nil {
		return Config{}, err
	}
	if err := readPositiveInt(table, "budget_tokens", &cfg.BudgetTokens); err != nil {
		return Config{}, err
	}
	if err := readUnitFloat(table, "min_score", &cfg.MinScore); err != nil {
		return Config{}, err
	}
	if err := readSeconds(table, "timeout_seconds", &cfg.Timeout); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// key renders a fully-qualified key name for an error message, so a user
// reading it knows which table to open.
func key(name string) string { return ParentSectionName + "." + SectionName + "." + name }

func readBool(table map[string]interface{}, name string, into *bool) error {
	raw, ok := table[name]
	if !ok || raw == nil {
		return nil
	}
	value, ok := raw.(bool)
	if !ok {
		return cascade.Newf(cascade.KindInvalidInput, "config: %s is %T, want a boolean", key(name), raw)
	}
	*into = value
	return nil
}

func readPositiveInt(table map[string]interface{}, name string, into *int) error {
	raw, ok := table[name]
	if !ok || raw == nil {
		return nil
	}
	value, err := asInt(raw)
	if err != nil {
		return cascade.Newf(cascade.KindInvalidInput, "config: %s is %T, want an integer", key(name), raw)
	}
	if value <= 0 {
		return cascade.Newf(cascade.KindInvalidInput, "config: %s is %d, want a positive integer", key(name), value)
	}
	*into = value
	return nil
}

func readUnitFloat(table map[string]interface{}, name string, into *float64) error {
	raw, ok := table[name]
	if !ok || raw == nil {
		return nil
	}
	value, err := asFloat(raw)
	if err != nil {
		return cascade.Newf(cascade.KindInvalidInput, "config: %s is %T, want a number", key(name), raw)
	}
	if value < 0 || value > 1 {
		return cascade.Newf(cascade.KindInvalidInput,
			"config: %s is %v, want a score between 0 and 1", key(name), value)
	}
	*into = value
	return nil
}

func readSeconds(table map[string]interface{}, name string, into *time.Duration) error {
	var seconds int
	if err := readPositiveInt(table, name, &seconds); err != nil {
		return err
	}
	if seconds > 0 {
		*into = time.Duration(seconds) * time.Second
	}
	return nil
}

// asInt accepts the integer spellings a TOML decoder produces.
func asInt(raw interface{}) (int, error) {
	switch v := raw.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		if v == float64(int(v)) {
			return int(v), nil
		}
	}
	return 0, cascade.New(cascade.KindInvalidInput, "not an integer")
}

// asFloat accepts the number spellings a TOML decoder produces. An integer
// is a valid score (0 and 1 are both meaningful bounds), so it is accepted
// rather than refused for not carrying a decimal point.
func asFloat(raw interface{}) (float64, error) {
	switch v := raw.(type) {
	case float64:
		return v, nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	}
	return 0, cascade.New(cascade.KindInvalidInput, "not a number")
}
