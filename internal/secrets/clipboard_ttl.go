// Purpose: the [secrets] clipboard_ttl range gate. The value reaching it is
//
//	the admitted one from runtime.SecretsClipboardTTL (the guarded-family
//	accessor), never a raw config read.
//
// Inputs: a [secrets] section view holding at most clipboard_ttl.
// Outputs: ParseClipboardTTL.
// Constraints: absent is 30s; anything outside [5s, 10m], or not a
//
//	duration, refuses with KindInvalidInput naming secrets.clipboard_ttl.
//
// SPORT: internal/secrets ParseClipboardTTL/ADDED (P1-SEC-09).

package secrets

import (
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	minClipboardTTL = 5 * time.Second
	maxClipboardTTL = 10 * time.Minute
)

// ParseClipboardTTL returns section's clipboard_ttl: a time.Duration, a
// duration string ("45s") or whole seconds. Absent is the shipped 30s.
func ParseClipboardTTL(section map[string]interface{}) (time.Duration, error) {
	raw, ok := section["clipboard_ttl"]
	if !ok || raw == nil {
		return runtime.DefaultSecretsClipboardTTL, nil
	}
	var d time.Duration
	switch v := raw.(type) {
	case time.Duration:
		d = v
	case string:
		p, err := time.ParseDuration(v)
		if err != nil {
			return 0, cascade.Newf(cascade.KindInvalidInput, "secrets.clipboard_ttl %q is not a duration", v)
		}
		d = p
	case int64:
		d = secondsTTL(v)
	case int:
		d = secondsTTL(int64(v))
	default:
		return 0, cascade.Newf(cascade.KindInvalidInput, "secrets.clipboard_ttl must be a duration, got %T", raw)
	}
	if err := checkClipboardTTL(d); err != nil {
		return 0, err
	}
	return d, nil
}

// secondsTTL converts whole seconds, mapping anything that would overflow
// to an out-of-range value instead of wrapping.
func secondsTTL(s int64) time.Duration {
	if s < 0 || s > int64(maxClipboardTTL/time.Second) {
		return -1
	}
	return time.Duration(s) * time.Second
}

func checkClipboardTTL(d time.Duration) error {
	if d < minClipboardTTL || d > maxClipboardTTL {
		return cascade.Newf(cascade.KindInvalidInput,
			"secrets.clipboard_ttl must be between %s and %s, got %s", minClipboardTTL, maxClipboardTTL, d)
	}
	return nil
}
