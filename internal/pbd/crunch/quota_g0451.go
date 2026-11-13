package crunch

import (
	"errors"
	"fmt"
)

// Alloc represents a quota allocation with account and percentage limits.
type Alloc struct {
	Account     string
	FiveHourPct int
	WeeklyPct   int
}

// ErrT0308 is returned when a quota mapping request is invalid.
var ErrT0308 = errors.New("T0308")

// MapQuota maps an account and percentage to an allocation.
// Valid account strings are exactly A3, A4, CO (case-sensitive, no trimming).
// The pct argument must be 0..100 inclusive.
// Invalid or empty account strings, or out-of-range percentages, return
// an empty Alloc and ErrT0308.
func MapQuota(acct string, pct int, _ bool) (Alloc, error) {
	if pct < 0 || pct > 100 {
		return Alloc{}, ErrT0308
	}

	switch acct {
	case "A3", "CO":
		return Alloc{Account: acct, FiveHourPct: pct, WeeklyPct: pct}, nil
	case "A4":
		if pct < 90 {
			return Alloc{}, ErrT0308
		}
		// 90/90 applied to A4
		return Alloc{Account: acct, FiveHourPct: 90, WeeklyPct: 90}, nil
	default:
		return Alloc{}, ErrT0308
	}
}

// FormatAllocation returns a formatted log string for the given allocation.
// It produces exactly "account=%s five_hour=%d weekly=%d" without a trailing newline.
// It is a pure formatter of any Alloc, including zero values, and performs no logging or validation.
func FormatAllocation(a Alloc) string {
	return fmt.Sprintf("account=%s five_hour=%d weekly=%d", a.Account, a.FiveHourPct, a.WeeklyPct)
}
