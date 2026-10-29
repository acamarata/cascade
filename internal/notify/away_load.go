// Purpose: ParseAwaySection, the package-owned reader of the [notify] away
//
//	keys. The daemon's config loader keeps every section it does not type in
//	Config.Extra, so this file owns the key table (the SUP pattern): which
//	keys exist, their types and their defaults.
//
// Inputs: the raw value of cfg.Extra["notify"] (a table, or nil when the
//
//	section is absent).
//
// Outputs: a validated AwayConfig, or a KindInvalidInput error naming the key.
// Constraints: strict. An unknown key, a value of the wrong type or a value
//
//	AwayConfig.Validate refuses is an error and yields the zero AwayConfig, so
//	the caller never installs half of a bad table. Reading is pure: no clock,
//	no environment, no I/O.
//
// SPORT: internal.notify.ParseAwaySection/ADDED (P1-HRN-11).

package notify

import (
	"math"
	"sort"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The [notify] keys this reader owns.
const (
	keyAwayThreshold     = "away_threshold"
	keyDigestUrgentLinks = "digest_urgent_deeplinks"
)

// ParseAwaySection reads the away keys out of cfg.Extra["notify"]. A nil raw
// is the absent section and returns DefaultAwayConfig. away_threshold is a
// duration string such as "30m"; digest_urgent_deeplinks is a whole number.
func ParseAwaySection(raw any) (AwayConfig, error) {
	if raw == nil {
		return DefaultAwayConfig(), nil
	}
	table, ok := raw.(map[string]any)
	if !ok {
		return AwayConfig{}, cascade.Newf(cascade.KindInvalidInput, "notify: the [notify] section must be a table, got %T", raw)
	}
	cfg := DefaultAwayConfig()
	keys := make([]string, 0, len(table))
	for k := range table {
		keys = append(keys, k)
	}
	sort.Strings(keys) // the first error is the same on every run
	for _, key := range keys {
		var err error
		switch key {
		case keyAwayThreshold:
			cfg.Threshold, err = durationValue(table[key])
		case keyDigestUrgentLinks:
			cfg.DigestUrgentDeepLinks, err = wholeNumberValue(table[key])
		default:
			err = cascade.Newf(cascade.KindInvalidInput, "notify: unknown [notify] key %q (away mode reads %s and %s)",
				key, keyAwayThreshold, keyDigestUrgentLinks)
		}
		if err != nil {
			return AwayConfig{}, err
		}
	}
	if err := cfg.Validate(); err != nil {
		return AwayConfig{}, err
	}
	return cfg, nil
}

// durationValue decodes a duration string.
func durationValue(v any) (time.Duration, error) {
	s, ok := v.(string)
	if !ok {
		return 0, cascade.Newf(cascade.KindInvalidInput, "notify: %s must be a duration string such as \"30m\", got %T", keyAwayThreshold, v)
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, cascade.Wrapf(cascade.KindInvalidInput, err, "notify: %s %q is not a duration", keyAwayThreshold, s)
	}
	return d, nil
}

// wholeNumberValue decodes an integer. The integral float64 a JSON round trip
// produces is accepted; a fraction, a bool and a string are not.
func wholeNumberValue(v any) (int, error) {
	switch n := v.(type) {
	case int:
		if n >= math.MinInt32 && n <= math.MaxInt32 {
			return n, nil
		}
	case int64:
		if n >= math.MinInt32 && n <= math.MaxInt32 {
			return int(n), nil
		}
	case float64:
		if n == math.Trunc(n) && math.Abs(n) <= math.MaxInt32 {
			return int(n), nil
		}
	}
	return 0, cascade.Newf(cascade.KindInvalidInput, "notify: %s must be a whole number, got %v", keyDigestUrgentLinks, v)
}
