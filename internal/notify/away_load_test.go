package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestParseAwaySectionDefaultsAndValidation: an absent section is the
// contract defaults, a good table is read key by key, and every refused input
// is KindInvalidInput with the zero AwayConfig (never half of a bad table).
func TestParseAwaySectionDefaultsAndValidation(t *testing.T) {
	good := []struct {
		name string
		raw  any
		want AwayConfig
	}{
		{"absent section", nil, DefaultAwayConfig()},
		{"empty table", map[string]any{}, DefaultAwayConfig()},
		{"both keys", map[string]any{"away_threshold": "45m", "digest_urgent_deeplinks": int64(3)}, AwayConfig{Threshold: 45 * time.Minute, DigestUrgentDeepLinks: 3}},
		{"threshold only", map[string]any{"away_threshold": "90s"}, AwayConfig{Threshold: 90 * time.Second, DigestUrgentDeepLinks: 10}},
		{"links only, zero allowed", map[string]any{"digest_urgent_deeplinks": 0}, AwayConfig{Threshold: 30 * time.Minute, DigestUrgentDeepLinks: 0}},
		{"integral float from a JSON round trip", map[string]any{"digest_urgent_deeplinks": float64(7)}, AwayConfig{Threshold: 30 * time.Minute, DigestUrgentDeepLinks: 7}},
	}
	for _, tc := range good {
		got, err := ParseAwaySection(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("%s: ParseAwaySection = %+v, %v; want %+v, nil", tc.name, got, err, tc.want)
		}
	}

	bad := []struct {
		name string
		raw  any
	}{
		{"zero threshold", map[string]any{"away_threshold": "0s"}},
		{"negative threshold", map[string]any{"away_threshold": "-5m"}},
		{"threshold not a duration", map[string]any{"away_threshold": "soon"}},
		{"threshold as a number", map[string]any{"away_threshold": 30}},
		{"negative links", map[string]any{"digest_urgent_deeplinks": -1}},
		{"links fraction", map[string]any{"digest_urgent_deeplinks": 2.5}},
		{"links as a string", map[string]any{"digest_urgent_deeplinks": "10"}},
		{"links as a bool", map[string]any{"digest_urgent_deeplinks": true}},
		{"links beyond int32", map[string]any{"digest_urgent_deeplinks": int64(1) << 40}},
		{"section not a table", "away_threshold = 30m"},
		{"section a list", []any{"away_threshold"}},
	}
	for _, tc := range bad {
		got, err := ParseAwaySection(tc.raw)
		if err == nil || !cascade.HasKind(err, cascade.KindInvalidInput) || got != (AwayConfig{}) {
			t.Errorf("%s: ParseAwaySection = %+v, %v; want the zero config and KindInvalidInput", tc.name, got, err)
		}
	}
}

// TestParseAwaySectionRejectsUnknownKey: a key this reader does not own
// refuses the whole table even next to valid keys, names the key, and the
// first error is the same on every run.
func TestParseAwaySectionRejectsUnknownKey(t *testing.T) {
	raw := map[string]any{"away_threshold": "5m", "away_treshold": "5m", "zzz": 1}
	for i := 0; i < 20; i++ {
		got, err := ParseAwaySection(raw)
		if err == nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("ParseAwaySection(unknown key) err = %v, want KindInvalidInput", err)
		}
		if got != (AwayConfig{}) {
			t.Fatalf("ParseAwaySection(unknown key) = %+v, want the zero config", got)
		}
		if !strings.Contains(err.Error(), `"away_treshold"`) {
			t.Fatalf("error %q does not name the first unknown key in sorted order", err)
		}
	}
}
