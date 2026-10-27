package runtime

// Purpose: the [secrets] detector thresholds as guarded config. entropy_floor
//
//	and confidence_threshold join EffectiveConfig: a RAISE of either is a
//	loosening (fewer strings are caught), a lowering is a tightening, and an
//	absent key is the shipped default. On a hot reload a tightening reaches
//	the live process detector through the installed applier before the new
//	config becomes current; a loosening is applied only after the
//	P1-SEC-04 elevation seam approved it (config.accept_loosening's slot).
//
// Inputs: the [secrets] table of two configs, and an applier function value
//
//	(internal/secrets imports this package, so the applier cannot be a
//	secrets type here).
//
// Outputs: LooseningPath values for CompareSecurity, the applier call inside
//
//	Reload, and the degraded-boot helpers baseline.go and the daemon use.
//
// Constraints: fail closed. A value that is not a finite number (a string, a
//
//	bool, NaN, Inf) reads as invalidThreshold, looser than every valid value,
//	so it can never ride through as a tightening; the applier then refuses it
//	too. An applier error rejects the reload and changes neither the config
//	nor the detector. Reload is serialised (HotReloader.mu) so two reloads
//	cannot apply to the detector in one order and store in the other.
//
// SPORT: runtime/hot-reload-engine (CHANGED, P1-SEC-24).

import (
	"context"
	"encoding/json"
	"math"
	"reflect"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The shipped [secrets] defaults (08-INIT-CONFIG-SPEC §3). internal/secrets
// aliases them, so the detector and the guard cannot disagree on what an
// absent key means.
const (
	DefaultSecretsEntropyFloor        = 3.5
	DefaultSecretsConfidenceThreshold = 0.8
)

// invalidThreshold stands for a threshold value that is not a finite number.
// It is finite so the baseline hash can encode it, and larger than any valid
// threshold (the bounds are 8 and 1), so it compares as a loosening.
const invalidThreshold = math.MaxFloat64

// Dotted keys of the two guarded thresholds.
const (
	keyEntropyFloor        = "secrets.entropy_floor"
	keyConfidenceThreshold = "secrets.confidence_threshold"
)

// secretsSectionAt reads the guarded [secrets] keys out of extra.
func secretsSectionAt(extra map[string]interface{}) SecretsSection {
	m := sectionAt(extra, "secrets")
	return SecretsSection{
		KeychainBackend:     stringAt(extra, "secrets", "keychain_backend"),
		EntropyFloor:        thresholdAt(m, "entropy_floor", DefaultSecretsEntropyFloor),
		ConfidenceThreshold: thresholdAt(m, "confidence_threshold", DefaultSecretsConfidenceThreshold),
	}
}

// The detector's valid ranges (secrets.DetectionConfig.Validate): entropy_floor
// in [0, 8], confidence_threshold in (0, 1]. A test pins these to Validate.
const maxSecretsEntropyFloor = 8.0

// thresholdValid reports whether v is a usable value for the key's detector range.
func thresholdValid(key string, v float64) bool {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return false
	}
	if key == "entropy_floor" {
		return v >= 0 && v <= maxSecretsEntropyFloor
	}
	return v > 0 && v <= 1
}

// thresholdAt returns nil for an absent key or one equal to def, a pointer to
// the value for a number inside the detector's range, and invalidThreshold for
// anything else: a string, a bool, NaN, Inf, or a number the detector refuses.
// An invalid value is therefore never a tightening, so it can neither be
// attested into the baseline nor become Effective or Current.
func thresholdAt(section map[string]interface{}, key string, def float64) *float64 {
	raw, ok := section[key]
	if !ok {
		return nil
	}
	v := invalidThreshold
	switch n := raw.(type) {
	case float64:
		v = n
	case int64:
		v = float64(n)
	case int:
		v = float64(n)
	}
	if !thresholdValid(key, v) {
		v = invalidThreshold
	}
	if v == def {
		return nil
	}
	return &v
}

// resolveThreshold maps nil to the default.
func resolveThreshold(p *float64, def float64) float64 {
	if p == nil {
		return def
	}
	return *p
}

// compareSecretsThresholds reports each threshold that proposed raises above
// current, after resolving an absent value to the default on both sides.
// Removing a tightened key is therefore a raise.
func compareSecretsThresholds(current, proposed SecretsSection) []LooseningPath {
	var paths []LooseningPath
	paths = append(paths, compareThreshold(keyEntropyFloor, current.EntropyFloor, proposed.EntropyFloor, DefaultSecretsEntropyFloor)...)
	paths = append(paths, compareThreshold(keyConfidenceThreshold, current.ConfidenceThreshold, proposed.ConfidenceThreshold, DefaultSecretsConfidenceThreshold)...)
	return paths
}

func compareThreshold(key string, current, proposed *float64, def float64) []LooseningPath {
	cur, prop := resolveThreshold(current, def), resolveThreshold(proposed, def)
	if prop <= cur {
		return nil
	}
	path := LooseningPath{Family: "secrets", Key: key, Current: cur, Proposed: prop,
		Reason: "a higher threshold catches fewer strings; an absent key is the shipped default"}
	if prop == invalidThreshold {
		path.Proposed, path.Reason = "invalid", "the value is not a number inside the detector's range; it is never read as a tightening and can never be accepted"
	}
	return []LooseningPath{path}
}

// SetSecretsApplier installs the function Reload calls with the proposed
// [secrets] table before the new config becomes current. The daemon installs
// the one that reloads the process detector in place. Without one (a library
// or daemonless caller) a [secrets] change is guarded by CompareSecurity only:
// it updates the snapshot and nothing validates or applies it.
func (hr *HotReloader) SetSecretsApplier(apply func(section map[string]interface{}) error) {
	hr.mu.Lock()
	defer hr.mu.Unlock()
	hr.secretsApply = apply
}

// applySecrets calls the applier when [secrets] differs from old's and
// reports whether it did. It runs before the new config becomes current, so
// the detector changes first. An error rejects the reload (event and audit
// row) and keeps both as they were. Caller holds hr.mu.
func (hr *HotReloader) applySecrets(ctx context.Context, old, candidate *Config) (bool, error) {
	if hr.secretsApply == nil {
		return false, nil
	}
	oldRaw, newRaw := old.Extra["secrets"], candidate.Extra["secrets"]
	if reflect.DeepEqual(oldRaw, newRaw) && !hr.secretsRetry {
		return false, nil
	}
	section := map[string]interface{}{}
	if newRaw != nil {
		table, ok := newRaw.(map[string]interface{})
		if !ok {
			err := cascade.New(cascade.KindInvalidInput, "secrets: [secrets] must be a table")
			hr.secretsRetry = true
			hr.reject(ctx, err.Error(), nil)
			return false, err
		}
		section = table
	}
	if err := hr.secretsApply(section); err != nil {
		// A failed apply may have left the subsystem marked failed: the next
		// reload applies again even if the table equals the running one.
		hr.secretsRetry = true
		hr.reject(ctx, err.Error(), nil)
		return false, err
	}
	hr.secretsRetry = false
	return true, nil
}

// restrictiveSecrets is the [secrets] a degraded boot runs: the shipped
// defaults, except that a threshold the operator had set tighter than the
// default stays (tightest of the two, per field). The zero values the other
// families use here would be refused by DetectionConfig.Validate, so absent
// stands for the default.
func restrictiveSecrets(onDisk SecretsSection) SecretsSection {
	return SecretsSection{
		EntropyFloor:        tighterThanDefault(onDisk.EntropyFloor, DefaultSecretsEntropyFloor),
		ConfidenceThreshold: tighterThanDefault(onDisk.ConfidenceThreshold, DefaultSecretsConfidenceThreshold),
	}
}

func tighterThanDefault(p *float64, def float64) *float64 {
	if p == nil || !(*p < def) {
		return nil
	}
	v := *p
	return &v
}

// ApplySecretsThresholds writes s's two thresholds into cfg (nil removes the
// key, so it reads as the default), copying every map it changes, the way
// ApplyEffective does. The daemon uses it to make a degraded boot's config
// carry the effective thresholds, not the on-disk ones.
func ApplySecretsThresholds(cfg *Config, s SecretsSection) {
	if cfg == nil {
		return
	}
	extra := make(map[string]interface{}, len(cfg.Extra)+1)
	for k, v := range cfg.Extra {
		extra[k] = v
	}
	sec := cloneSection(extra, "secrets")
	setThresholdKey(sec, "entropy_floor", s.EntropyFloor)
	setThresholdKey(sec, "confidence_threshold", s.ConfidenceThreshold)
	if len(sec) > 0 || sectionAt(extra, "secrets") != nil {
		extra["secrets"] = sec
	}
	cfg.Extra = extra
}

func setThresholdKey(section map[string]interface{}, key string, p *float64) {
	if p == nil {
		delete(section, key)
		return
	}
	section[key] = *p
}

// BootEffective returns the guarded values the last CheckFile told the
// caller to run with. ok is false when no check ran.
func (b *BaselineChecker) BootEffective() (EffectiveConfig, bool) {
	if b == nil || b.boot == nil {
		return EffectiveConfig{}, false
	}
	return *b.boot, true
}

// secretsPlain has SecretsSection's fields and none of its methods.
type secretsPlain SecretsSection

// validate refuses a section holding an invalid threshold. The message says
// what to do, since the caller is an operator accepting a config file.
func (s SecretsSection) validate() error {
	for _, t := range []*float64{s.EntropyFloor, s.ConfidenceThreshold} {
		if t != nil && *t == invalidThreshold {
			return cascade.New(cascade.KindInvalidInput,
				"secrets: a [secrets] threshold is not a number inside the detector's range (entropy_floor 0 to 8, confidence_threshold above 0 up to 1); fix the value in config.toml. Nothing was recorded")
		}
	}
	return nil
}

// MarshalJSON encodes the section for the baseline hash and snapshot and
// refuses one holding an invalid threshold. The refusal is what keeps an
// invalid value out of the persisted baseline on every path (boot,
// config.accept_loosening): the encode that precedes any store write fails.
// A valid section encodes exactly as the plain struct always has.
func (s SecretsSection) MarshalJSON() ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(secretsPlain(s))
}
