// Purpose: the detector's two ratified configuration keys - the
//
//	08-INIT-CONFIG-SPEC §3 [secrets] rows entropy_floor and
//	confidence_threshold (R-16.47) - with their defaults, their
//	validation, and the atomic holder that makes a hot reload safe
//	against a concurrent Scan.
//
// Inputs: a decoded [secrets] TOML section as a generic map, or a
//
//	DetectionConfig value built directly.
//
// Outputs: a validated DetectionConfig, or a pkg/cascade
//
//	KindInvalidInput error naming the offending key and its bound.
//
// Constraints: validation is FAIL-CLOSED and total - an unparseable or
//
//	out-of-range value is refused, never clamped and never defaulted
//	past. A reload that fails leaves the previous configuration running
//	(Detector.Reload), because "the old rules still apply" is a safe
//	degradation and "no rules apply" is not. No I/O here: the caller
//	reads config.toml, this file only interprets what it decoded.
//
// SPORT: SECRETS_DETECTOR: ADD (internal/secrets.DetectionConfig).

package secrets

import (
	"math"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The ratified [secrets] defaults (08-INIT-CONFIG-SPEC §3, R-16.47).
const (
	// DefaultEntropyFloor is the Shannon-entropy floor, in bits per
	// character, below which a run is not even a candidate.
	DefaultEntropyFloor = 3.5
	// DefaultConfidenceThreshold is the score a hit must reach to be
	// eligible for quarantine. It sits above ConfidenceStructured and
	// below ConfidenceCorroborated on purpose: corroborated hits are
	// quarantined, shape-only hits are not.
	DefaultConfidenceThreshold = 0.8
)

// maxEntropyFloor is the ceiling on entropy_floor. Eight bits per
// character is the maximum a byte alphabet can carry, so a higher floor
// would disable the entropy signal entirely while looking like a setting.
const maxEntropyFloor = 8.0

// DetectionConfig is the detector's runtime configuration.
type DetectionConfig struct {
	// EntropyFloor is the bits-per-character floor for the entropy
	// signal, in [0, 8].
	EntropyFloor float64 `json:"entropy_floor"`
	// ConfidenceThreshold is the score a hit must reach to be
	// quarantined, in (0, 1].
	ConfidenceThreshold float64 `json:"confidence_threshold"`
}

// DefaultDetectionConfig returns the ratified defaults.
func DefaultDetectionConfig() DetectionConfig {
	return DetectionConfig{
		EntropyFloor:        DefaultEntropyFloor,
		ConfidenceThreshold: DefaultConfidenceThreshold,
	}
}

// Validate reports whether the configuration is usable. Both bounds are
// refusals rather than clamps: an operator who wrote confidence_threshold
// = 0 asked for something the detector will not do, and silently running
// at 0.8 instead would hide that.
func (c DetectionConfig) Validate() error {
	if c.EntropyFloor < 0 || c.EntropyFloor > maxEntropyFloor || math.IsNaN(c.EntropyFloor) {
		return cascade.Newf(cascade.KindInvalidInput,
			"secrets: [secrets] entropy_floor must be between 0 and %g bits per character, got %v",
			maxEntropyFloor, c.EntropyFloor)
	}
	if !(c.ConfidenceThreshold > 0) || c.ConfidenceThreshold > 1 || math.IsNaN(c.ConfidenceThreshold) {
		return cascade.Newf(cascade.KindInvalidInput,
			"secrets: [secrets] confidence_threshold must be greater than 0 and at most 1, got %v",
			c.ConfidenceThreshold)
	}
	return nil
}

// ParseDetectionConfig builds a DetectionConfig from a decoded [secrets]
// TOML section. An absent key takes its ratified default; a key that is
// present but not a number is refused, because a typo'd threshold that
// silently reverts to the default is a security setting that lies.
func ParseDetectionConfig(section map[string]interface{}) (DetectionConfig, error) {
	cfg := DefaultDetectionConfig()
	if raw, ok := section["entropy_floor"]; ok {
		value, err := configFloat("entropy_floor", raw)
		if err != nil {
			return DetectionConfig{}, err
		}
		cfg.EntropyFloor = value
	}
	if raw, ok := section["confidence_threshold"]; ok {
		value, err := configFloat("confidence_threshold", raw)
		if err != nil {
			return DetectionConfig{}, err
		}
		cfg.ConfidenceThreshold = value
	}
	if err := cfg.Validate(); err != nil {
		return DetectionConfig{}, err
	}
	return cfg, nil
}

// configFloat coerces the number shapes a TOML decoder produces. A bool,
// a string or a table is refused: TOML has a number syntax, and a
// "0.8"-as-string in a security setting is a mistake worth surfacing.
func configFloat(key string, raw interface{}) (float64, error) {
	switch v := raw.(type) {
	case float64:
		return v, nil
	case int64:
		return float64(v), nil
	case int:
		return float64(v), nil
	default:
		return 0, cascade.Newf(cascade.KindInvalidInput,
			"secrets: [secrets] %s must be a number, got %T", key, raw)
	}
}

// atomicConfig holds the running DetectionConfig. A plain mutex rather
// than sync/atomic: DetectionConfig is two floats, the read is on the
// scan path but not in an inner loop (Scan loads it once per call), and a
// mutex keeps the two fields consistent with each other - a torn read
// that paired the old floor with the new threshold would be a silent
// mis-scan nobody could reproduce.
type atomicConfig struct {
	mu    sync.RWMutex
	value DetectionConfig
}

// store replaces the held configuration.
func (a *atomicConfig) store(cfg DetectionConfig) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.value = cfg
}

// load returns the held configuration.
func (a *atomicConfig) load() DetectionConfig {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.value
}
