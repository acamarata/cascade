package supervision

import (
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestParseStallThresholdDefaultAndBounds(t *testing.T) {
	if got, err := ParseStallThreshold(nil); err != nil || got != 30*time.Minute {
		t.Errorf("absent = (%v, %v), want (30m, nil)", got, err)
	}
	if got, err := ParseStallThreshold(map[string]interface{}{"other": 1}); err != nil || got != 30*time.Minute {
		t.Errorf("absent key = (%v, %v), want 30m", got, err)
	}
	ok := map[string]time.Duration{"1m": time.Minute, "45m": 45 * time.Minute, "24h": 24 * time.Hour, "90s": 90 * time.Second}
	for text, want := range ok {
		if got, err := ParseStallThreshold(map[string]interface{}{"stall_threshold": text}); err != nil || got != want {
			t.Errorf("%q = (%v, %v), want %v", text, got, err, want)
		}
	}
	for _, bad := range []interface{}{"59s", "24h1s", "0s", "-5m", "abc", "", 30, true, nil} {
		got, err := ParseStallThreshold(map[string]interface{}{"stall_threshold": bad})
		if got != 0 || !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("%v = (%v, %v), want (0, InvalidInput)", bad, got, err)
		}
	}
}
