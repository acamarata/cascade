package topology

import (
	"testing"
	"time"
)

// FuzzParseRetryAfter drives ParseRetryAfter with arbitrary bytes: it must
// never panic, and any (duration, true) result must carry a non-negative
// duration (06 Sec.5 rule 7). Seed corpus: testdata/fuzz/FuzzParseRetryAfter/.
func FuzzParseRetryAfter(f *testing.F) {
	for _, seed := range []string{
		"", "0", "120", "-1", "not-a-value",
		"Mon, 02 Jan 2006 15:04:05 GMT",
		"Fri, 12 Sep 2026 00:01:30 GMT",
		"99999999999999999999999999",
		"\x00\x01\x02",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, v string) {
		now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
		d, ok := ParseRetryAfter(v, now)
		if ok && d < 0 {
			t.Fatalf("ParseRetryAfter(%q) returned a negative duration %v with ok=true", v, d)
		}
	})
}
