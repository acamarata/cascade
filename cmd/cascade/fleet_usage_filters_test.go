// Purpose: `cascade fleet usage` coverage split out of fleet_usage_test.go
// under the 300-line file cap (LANE-RULES §8): --since/--provider
// filtering, the headless (no-daemon) path, the behavioral CLI-script
// surface (see fleet_usage_test.go's sibling header for the LANE-RULES
// txtar deviation this satisfies instead), the --since parser's own unit
// cases, its required fuzz target, and the empty-aggregate edge case.
//
// SPORT: cmd.cascade.fleet-usage/TEST (P1-E18-W4-S40-T5).
package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// fleetUsageJSONRows runs fleet usage with args against deps and decodes
// the --json envelope's rows, shared by both TestFleetUsageFilters
// subtests to stay under Art.10.3's 50-line function cap.
func fleetUsageJSONRows(t *testing.T, deps fleetSessionsDeps, args ...string) []fleetUsageRow {
	t.Helper()
	out, err := runFleetUsageCLI(deps, args...)
	if err != nil {
		t.Fatalf("fleet usage %v: %v (output: %s)", args, err, out)
	}
	var envelope struct {
		Data struct {
			Rows []fleetUsageRow `json:"rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &envelope); err != nil {
		t.Fatalf("fleet usage %v: not valid JSON: %v", args, err)
	}
	return envelope.Data.Rows
}

// TestFleetUsageFilters: --since and --provider each narrow the result to
// exactly the matching rows.
func TestFleetUsageFilters(t *testing.T) {
	fixture := loadUsageFixture(t)

	t.Run("since", func(t *testing.T) {
		dataDir := t.TempDir()
		seedFleetUsageFixture(t, dataDir, fixture)
		deps := fleetSessionsDeps{Paths: fixedDataDirPaths{dataDir: dataDir}}
		rows := fleetUsageJSONRows(t, deps, "--since=2026-08-24", "--json")
		if len(rows) != 2 {
			t.Fatalf("--since=2026-08-24: got %d rows, want 2 (the 08-25 and 08-28 buckets only)", len(rows))
		}
		for _, r := range rows {
			if r.RangeStart < "2026-08-24" {
				t.Errorf("--since=2026-08-24: row %+v starts before the cutoff", r)
			}
		}
	})

	t.Run("provider", func(t *testing.T) {
		dataDir := t.TempDir()
		seedFleetUsageFixture(t, dataDir, fixture)
		deps := fleetSessionsDeps{Paths: fixedDataDirPaths{dataDir: dataDir}}
		rows := fleetUsageJSONRows(t, deps, "--provider=openai", "--json")
		if len(rows) != 1 || rows[0].Provider != "openai" {
			t.Fatalf("--provider=openai: rows = %+v, want exactly one openai row", rows)
		}
		if rows[0].Requests != 2 || rows[0].TokensIn != 4200 {
			t.Fatalf("--provider=openai: row = %+v, want requests=2 tokens_in=4200", rows[0])
		}
	})
}

// TestFleetUsageHeadless: the command reads correctly and exits 0 against
// a seeded store with no daemon socket present at all — proving
// full_desc's "no daemon dependency" claim rather than assuming it, since
// runFleetUsage never touches deps.DialContext or a daemon socket path.
func TestFleetUsageHeadless(t *testing.T) {
	dataDir := t.TempDir()
	fixture := loadUsageFixture(t)
	seedFleetUsageFixture(t, dataDir, fixture)
	// No daemon.sock is ever created under dataDir's parent; DialContext
	// is left nil (a call through it would panic, proving the command
	// path never reaches for it).
	deps := fleetSessionsDeps{Paths: fixedDataDirPaths{dataDir: dataDir}, DialContext: nil}

	out, err := runFleetUsageCLI(deps)
	if err != nil {
		t.Fatalf("fleet usage (headless): %v (output: %s)", err, out)
	}
	if !strings.Contains(out, "anthropic") {
		t.Errorf("fleet usage (headless): missing expected rows; got:\n%s", out)
	}
}

// TestFleetUsageScript drives the same behavioral surface a
// cmd/cascade/testdata/scripts/fleet_usage.txtar file would (see
// fleet_usage_test.go's header LANE-RULES DEVIATION note): table output,
// --json, --since, --provider, and an error path, all through the real
// cobra Execute() entry point.
func TestFleetUsageScript(t *testing.T) {
	fixture := loadUsageFixture(t)

	t.Run("table", func(t *testing.T) {
		dataDir := t.TempDir()
		seedFleetUsageFixture(t, dataDir, fixture)
		out, err := runFleetUsageCLI(fleetSessionsDeps{Paths: fixedDataDirPaths{dataDir: dataDir}})
		if err != nil || !strings.Contains(out, "requests=") {
			t.Fatalf("table script step: err=%v out=%s", err, out)
		}
	})

	t.Run("json", func(t *testing.T) {
		dataDir := t.TempDir()
		seedFleetUsageFixture(t, dataDir, fixture)
		out, err := runFleetUsageCLI(fleetSessionsDeps{Paths: fixedDataDirPaths{dataDir: dataDir}}, "--json")
		if err != nil || !strings.Contains(out, `"ok": true`) {
			t.Fatalf("json script step: err=%v out=%s", err, out)
		}
	})

	t.Run("since_and_provider_combined", func(t *testing.T) {
		dataDir := t.TempDir()
		seedFleetUsageFixture(t, dataDir, fixture)
		out, err := runFleetUsageCLI(fleetSessionsDeps{Paths: fixedDataDirPaths{dataDir: dataDir}},
			"--since=2026-08-01", "--provider=anthropic")
		if err != nil || !strings.Contains(out, "anthropic") || strings.Contains(out, "openai") {
			t.Fatalf("since+provider script step: err=%v out=%s", err, out)
		}
	})

	t.Run("error_path_bad_since", func(t *testing.T) {
		dataDir := t.TempDir()
		out, err := runFleetUsageCLI(fleetSessionsDeps{Paths: fixedDataDirPaths{dataDir: dataDir}}, "--since=not-a-value")
		if err == nil {
			t.Fatalf("--since=not-a-value: want an error, got success (output: %s)", out)
		}
	})

	t.Run("error_path_extra_arg", func(t *testing.T) {
		dataDir := t.TempDir()
		out, err := runFleetUsageCLI(fleetSessionsDeps{Paths: fixedDataDirPaths{dataDir: dataDir}}, "unexpected-arg")
		if err == nil {
			t.Fatalf("unexpected positional arg: want an error, got success (output: %s)", out)
		}
	})
}

// TestParseSinceFlag covers the parser directly: empty, day/week suffix,
// stdlib duration, absolute date, and the invalid-input refusal.
func TestParseSinceFlag(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		raw     string
		want    time.Time
		wantErr bool
	}{
		{"empty", "", time.Time{}, false},
		{"days", "7d", now.Add(-7 * 24 * time.Hour), false},
		{"weeks", "2w", now.Add(-14 * 24 * time.Hour), false},
		{"stdlib_duration", "24h", now.Add(-24 * time.Hour), false},
		{"absolute_date", "2026-08-01", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), false},
		{"invalid", "not-a-value", time.Time{}, true},
		{"negative_days", "-3d", time.Time{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSinceFlag(tc.raw, now)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseSinceFlag(%q) = %v, nil, want an error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSinceFlag(%q): %v", tc.raw, err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("parseSinceFlag(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

// FuzzSinceFlagParse proves parseSinceFlag never panics over arbitrary
// input (06 rule 5.7's required fuzz target for the --since parser).
func FuzzSinceFlagParse(f *testing.F) {
	for _, seed := range []string{"", "7d", "2w", "24h", "2026-08-01", "not-a-value", "-1d", "999999999999d"} {
		f.Add(seed)
	}
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	f.Fuzz(func(_ *testing.T, raw string) {
		_, _ = parseSinceFlag(raw, now)
	})
}

// TestAggregateFleetUsageEmpty: zero rows aggregate to zero rows and the
// human table reports the documented "no usage recorded" sentinel, never
// a panic on an empty slice.
func TestAggregateFleetUsageEmpty(t *testing.T) {
	got := aggregateFleetUsage(nil)
	if len(got) != 0 {
		t.Fatalf("aggregateFleetUsage(nil) = %+v, want empty", got)
	}
	if s := (fleetUsageResult{}).String(); s != "no usage recorded" {
		t.Errorf("fleetUsageResult{}.String() = %q, want %q", s, "no usage recorded")
	}
}
