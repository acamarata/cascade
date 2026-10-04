package runtime

// Purpose: TestLearnRetentionMaxAgeDaysParse proves [learn].retention.
//   max_age_days end to end through Load: an absent [learn.retention]
//   table parses to the zero value (internal/learn.RetentionMaxAgeDays
//   supplies the 90-day default from there, per config_learn.go's own
//   doc comment), an explicit positive value round-trips, and a
//   non-positive or unrecognised key refuses at config load
//   (P1-E31-W6-S64-T1).
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: reuses config_test.go's writeConfigFile/fakeEnviron
//   helpers (same package) rather than re-declaring them (C5).
// SPORT: internal.runtime.parseLearnSection/ADDED (test coverage).

import (
	"context"
	"testing"
)

func TestLearnRetentionMaxAgeDaysParse(t *testing.T) {
	cases := []struct {
		name       string
		toml       string
		wantMaxAge int
		wantErr    bool
	}{
		{name: "absent learn table parses to zero value", toml: "", wantMaxAge: 0},
		{name: "explicit positive value round-trips", toml: "[learn.retention]\nmax_age_days = 30\n", wantMaxAge: 30},
		{name: "zero refuses at load", toml: "[learn.retention]\nmax_age_days = 0\n", wantErr: true},
		{name: "negative refuses at load", toml: "[learn.retention]\nmax_age_days = -5\n", wantErr: true},
		{name: "unrecognised key refuses at load", toml: "[learn.retention]\nmax_age_days = 30\nbogus = 1\n", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeConfigFile(t, dir, tc.toml)
			cfg, err := Load(context.Background(), LoadOptions{
				Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil),
			})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Load = nil error, want refusal")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Learn.Retention.MaxAgeDays != tc.wantMaxAge {
				t.Errorf("Learn.Retention.MaxAgeDays = %d, want %d", cfg.Learn.Retention.MaxAgeDays, tc.wantMaxAge)
			}
		})
	}
}
