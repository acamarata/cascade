package runtime

// Purpose: the rest of the [retrieval] config surface — sources[],
//   [retrieval.fusion] k and weights (08-INIT-CONFIG-SPEC §3, R-14.18) —
//   and the accessors the retrieval pipeline reads them through. Split out
//   of config_retrieval.go rather than added to it per R-14.117/Art.10.3:
//   config_retrieval.go holds [retrieval.reranker] and adding this surface
//   to it would carry the file past the 300-line cap.
// Inputs: the decoded [retrieval] table.
// Outputs: the typed sources/fusion slices of retrievalSection, or a
//   *ConfigError.
// Constraints: every parse below FAILS CLOSED. An unknown key, a wrong
//   type, a duplicate source, a non-positive fusion.k and a negative or
//   non-finite weight are each a hard typed error that refuses the whole
//   config. Silently defaulting any of them would tell an operator who
//   configured retrieval that their tuning is live when it is not.
//   retrieval.fusion.enabled is DELIBERATELY not validated here: it is
//   F/S-12.T6's key end-to-end (R-16.49), so it is accepted and passed
//   over rather than rejected as unknown.
// SPORT: runtime/config.retrieval (ADD, P1-E06-W2-S12-T4).

import (
	"math"
	"strconv"
)

// DefaultFusionK is the shipped retrieval.fusion.k: 60, the canonical RRF
// smoothing constant (08 §3, 04 §Epic F preamble). It is declared here
// rather than read from internal/retrieval/rrf so the config package does
// not depend on the pipeline it configures; the two constants agreeing is
// asserted by internal/retrieval's config test rather than assumed.
const DefaultFusionK int64 = 60

// fusionSection is [retrieval.fusion]: the parameters the S-11.T1 RRF
// fusion consumes.
//
// K is 0 when the file did not set it, and FusionK resolves that to
// DefaultFusionK. The zero value is used as "unset" rather than
// pre-stamping the default into the struct so the effective-value
// resolution has exactly one home.
type fusionSection struct {
	// K is the RRF smoothing constant. fusion.k IS the RRF k (04 §Epic F
	// preamble); there is no separate top-K key.
	K int64
	// Weights are the per-leg fusion weights, by leg name. Nil when the
	// file set none, which leaves every leg at its neutral weight.
	Weights map[string]float64
}

// RetrievalSources returns the configured corpus sources, in file order.
// The slice is copied, so a caller cannot mutate the loaded config.
func (c *Config) RetrievalSources() []string {
	if c == nil || len(c.Retrieval.Sources) == 0 {
		return nil
	}
	return append([]string(nil), c.Retrieval.Sources...)
}

// FusionK returns the effective retrieval.fusion.k: the configured value,
// or DefaultFusionK when the file set none. It never returns a
// non-positive value — the parser refuses one — so a caller can pass the
// result straight to the fusion without re-checking it.
func (c *Config) FusionK() int64 {
	if c == nil || c.Retrieval.Fusion.K <= 0 {
		return DefaultFusionK
	}
	return c.Retrieval.Fusion.K
}

// FusionWeights returns the configured per-leg weights, copied. Nil means
// no weights were configured, which is not the same as every weight being
// zero: a zero weight silences a leg, and nil leaves each leg neutral.
func (c *Config) FusionWeights() map[string]float64 {
	if c == nil || len(c.Retrieval.Fusion.Weights) == 0 {
		return nil
	}
	out := make(map[string]float64, len(c.Retrieval.Fusion.Weights))
	for k, v := range c.Retrieval.Fusion.Weights {
		out[k] = v
	}
	return out
}

// parseSources reads retrieval.sources[] out of the [retrieval] table.
//
// A source is an opaque locator string (a path, or a name a later
// lifecycle surface resolves). Its CONTENT is not validated here — this
// package does not touch the filesystem — but its SHAPE is: a non-array,
// a non-string entry, an empty entry and a duplicate entry are each hard
// errors. A duplicate would make the same corpus ingest twice and rank
// twice; dropping it silently would be this package deciding what the
// operator meant.
func parseSources(table map[string]interface{}) ([]string, error) {
	raw, ok := table["sources"]
	if !ok {
		return nil, nil
	}
	list, ok := raw.([]interface{})
	if !ok {
		return nil, &ConfigError{Field: "retrieval.sources", Reason: "must be an array of strings"}
	}
	out := make([]string, 0, len(list))
	seen := make(map[string]bool, len(list))
	for i, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, &ConfigError{
				Field:  indexedField("retrieval.sources", i),
				Reason: "must be a string",
			}
		}
		if s == "" {
			return nil, &ConfigError{
				Field:  indexedField("retrieval.sources", i),
				Reason: "must not be empty",
			}
		}
		if seen[s] {
			return nil, &ConfigError{
				Field:  indexedField("retrieval.sources", i),
				Reason: "duplicates an earlier source",
			}
		}
		seen[s] = true
		out = append(out, s)
	}
	return out, nil
}

// indexedField renders a dotted field name with an array index, so an
// error names the offending entry rather than the whole array.
func indexedField(base string, i int) string {
	return base + "[" + strconv.Itoa(i) + "]"
}

// parseFusion reads [retrieval.fusion] out of the [retrieval] table.
//
// Recognised keys are k, weights, and enabled. enabled is F/S-12.T6's key
// end-to-end (R-16.49): it is accepted and skipped here, never
// re-declared and never re-validated, so the two tickets cannot disagree
// about its type or its default. Any other key is a hard error.
func parseFusion(table map[string]interface{}) (fusionSection, error) {
	out := fusionSection{}
	raw, ok := table["fusion"]
	if !ok {
		return out, nil
	}
	fusion, ok := raw.(map[string]interface{})
	if !ok {
		return out, &ConfigError{Field: "retrieval.fusion", Reason: "must be a table"}
	}
	for key := range fusion {
		switch key {
		case "k", "weights", "enabled":
		default:
			return out, &ConfigError{
				Field:  "retrieval.fusion." + key,
				Reason: "unrecognised key in [retrieval.fusion]",
			}
		}
	}
	k, err := parseFusionK(fusion)
	if err != nil {
		return out, err
	}
	out.K = k
	weights, err := parseFusionWeights(fusion)
	if err != nil {
		return out, err
	}
	out.Weights = weights
	return out, nil
}

// parseFusionK reads fusion.k. Zero and negative are refused rather than
// falling back to the default: k <= 0 makes the RRF denominator meet or
// cross zero, and the fusion itself refuses such a k, so accepting it here
// and defaulting it would hide a configuration the operator has to fix.
func parseFusionK(fusion map[string]interface{}) (int64, error) {
	raw, ok := fusion["k"]
	if !ok {
		return 0, nil
	}
	k, ok := raw.(int64)
	if !ok {
		return 0, &ConfigError{Field: "retrieval.fusion.k", Reason: "must be an integer"}
	}
	if k <= 0 {
		return 0, &ConfigError{Field: "retrieval.fusion.k", Reason: "must be greater than zero"}
	}
	return k, nil
}

// parseFusionWeights reads fusion.weights, the per-leg weight table.
//
// A weight must be a finite, non-negative number; an integer literal is
// accepted and widened, because `weights = { fts5 = 1 }` is what an
// operator writes and refusing it would be pedantry rather than safety. A
// negative weight would make a leg's evidence count AGAINST a chunk, which
// no leg means; NaN or an infinity would poison the whole ranking. Whether
// a named leg exists is decided by the fusion, which knows the legs.
func parseFusionWeights(fusion map[string]interface{}) (map[string]float64, error) {
	raw, ok := fusion["weights"]
	if !ok {
		return nil, nil
	}
	table, ok := raw.(map[string]interface{})
	if !ok {
		return nil, &ConfigError{Field: "retrieval.fusion.weights", Reason: "must be a table"}
	}
	out := make(map[string]float64, len(table))
	for leg, v := range table {
		if leg == "" {
			return nil, &ConfigError{Field: "retrieval.fusion.weights", Reason: "a leg name is empty"}
		}
		field := "retrieval.fusion.weights." + leg
		w, ok := toFloat(v)
		if !ok {
			return nil, &ConfigError{Field: field, Reason: "must be a number"}
		}
		if math.IsNaN(w) || math.IsInf(w, 0) || w < 0 {
			return nil, &ConfigError{Field: field, Reason: "must be a finite, non-negative number"}
		}
		out[leg] = w
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// toFloat widens the two numeric shapes a TOML decode produces.
func toFloat(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}
