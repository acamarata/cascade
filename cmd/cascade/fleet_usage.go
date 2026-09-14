// Purpose: `cascade fleet usage [--since] [--provider]` (P1-E18-W4-S40-T5,
// 07-CLI-COMMAND-TREE §fleet). Reads the J/S-20.T4 usage-accounting domain
// (internal/providers/usage) and renders aggregated rows: provider, model,
// request count, token sums, estimated cost, and the calendar date range
// the aggregate spans. Never dials the daemon — the domain lives entirely
// in local storage, so the command behaves identically whether or not a
// daemon is running (automation parity, 06 §5.8; full_desc's "works in
// both daemon and daemonless modes").
//
// Inputs: cobra args/flags; the same fleetSessionsDeps injection every
// other fleet subcommand in this package already uses (only .Paths is
// read, for the data directory).
//
// Outputs: process output via internal/output.Writer (the root's --json
// persistent flag selects the versioned envelope); a typed taxonomy error
// on failure. Never a bare fmt.Print.
//
// Constraints: read-only verb; no daemon RPC surface at all. Reuses
// provider_health_cmd.go's openMigratedDB helper and providerUsageDBFile
// constant (same package) rather than re-implementing SQLite bootstrap, so
// this command and `cascade provider usage` open the exact same on-disk
// database and can never disagree about where the data lives.
//
// CONTRACT DEVIATION (recorded, not papered over — R-16.79). full_desc
// says this command "reads accumulated per-provider, per-model usage
// accounting from the local cascade.db store." That is wrong on the tree
// as it stands today: internal/providers/usage/migration.go's own
// DATABASE CORRECTION (R-16.77) states the usage domain does NOT target
// cascade.db at all — it lives in a dedicated provider-usage.db file,
// exactly the file provider_health_cmd.go's openProviderStorage already
// opens for `cascade provider usage`. This file follows the tree, not the
// contract: it opens providerUsageDBFile, the same file and the same
// domain `cascade provider usage` already reads, adding the aggregation
// (per provider+model, across every day-bucket row, with a computed date
// range) and the --since/--provider filters that command does not have.
// The two commands intentionally share the read domain rather than
// duplicating it — DRY per the engineering standard.
//
// SPORT: fleet.usage_command/ADD (P1-E18-W4-S40-T5).
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/providers/usage"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// newFleetUsageCmd builds `fleet usage [--since] [--provider]`. --json is
// the root's persistent global flag (07 §global-flags), matching every
// other fleet subcommand — this command adds no --json flag of its own.
func newFleetUsageCmd(deps fleetSessionsDeps) *cobra.Command {
	var since, providerName string
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show aggregated per-provider, per-model usage and cost",
		Long: "Reads the local usage-accounting domain (no daemon dependency) and renders\n" +
			"aggregated rows: provider, model, request count, token sums, estimated\n" +
			"cost, and the calendar date range covered. Works identically with or\n" +
			"without a running daemon.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runFleetUsage(cmd, deps, since, providerName)
		},
	}
	cmd.Flags().StringVar(&since, "since", "",
		"only include usage on or after this cutoff (duration like 7d/24h, or date YYYY-MM-DD)")
	cmd.Flags().StringVar(&providerName, "provider", "", "restrict to a single provider name")
	return cmd
}

// runFleetUsage resolves --since, opens the usage domain, queries and
// aggregates, then writes the result through the standard output writer.
func runFleetUsage(cmd *cobra.Command, deps fleetSessionsDeps, since, providerName string) error {
	cutoff, err := parseSinceFlag(since, runtime.NewSystemClock().Now())
	if err != nil {
		return err
	}
	reader, closeDB, err := openFleetUsageReader(cmd.Context(), deps)
	if err != nil {
		return err
	}
	defer func() { _ = closeDB() }()

	rows, err := reader.QueryUsage(cmd.Context(), provider.UsageFilter{ProviderName: providerName, Since: cutoff})
	if err != nil {
		return err
	}
	return fleetSessionsOutputWriter(cmd).Result(fleetUsageResult{Rows: aggregateFleetUsage(rows)})
}

// openFleetUsageReader opens (creating and migrating on first use) the
// same provider-usage.db file openProviderStorage builds for `cascade
// provider usage`, and returns a ready *usage.Reader. The caller MUST call
// the returned close func.
func openFleetUsageReader(ctx context.Context, deps fleetSessionsDeps) (*usage.Reader, func() error, error) {
	dataDir := deps.Paths.DataDir()
	if dataDir == "" {
		return nil, nil, cascade.New(cascade.KindUnavailable, "fleet usage: could not resolve the cascade data directory")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "fleet usage: create data directory")
	}
	clock := runtime.NewSystemClock()
	db, err := openMigratedDB(ctx, filepath.Join(dataDir, providerUsageDBFile),
		func(ctx context.Context, db *sql.DB) error {
			return usage.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", "")
		})
	if err != nil {
		return nil, nil, err
	}
	return usage.NewReader(usage.NewManager(db, clock)), db.Close, nil
}

// parseSinceFlag parses --since's <duration|date> grammar relative to now:
// a bare "<N>d"/"<N>w" day/week suffix (Go's time.ParseDuration has
// neither unit), a stdlib duration string ("24h", "90m"), or an absolute
// "YYYY-MM-DD" date (UTC midnight). An empty string means "no
// restriction" (the zero time.Time, matching provider.UsageFilter's own
// zero-value convention). now is always caller-supplied (Art.7.3: no bare
// time.Now here) — runFleetUsage passes runtime.NewSystemClock().Now().
func parseSinceFlag(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	if d, ok := parseDayWeekSuffix(raw); ok {
		return now.Add(-d), nil
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return now.Add(-d), nil
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t, nil
	}
	return time.Time{}, cascade.Newf(cascade.KindInvalidInput,
		"fleet usage: invalid --since value %q (want a duration like 7d/24h, or a date YYYY-MM-DD)", raw)
}

// parseDayWeekSuffix parses a "<non-negative integer>d" or "...w" string.
// Never panics on adversarial input (FuzzSinceFlagParse's own contract):
// every branch returns (0, false) rather than indexing past a check.
func parseDayWeekSuffix(raw string) (time.Duration, bool) {
	if len(raw) < 2 {
		return 0, false
	}
	unit := raw[len(raw)-1]
	if unit != 'd' && unit != 'w' {
		return 0, false
	}
	n, err := strconv.Atoi(raw[:len(raw)-1])
	if err != nil || n < 0 {
		return 0, false
	}
	if unit == 'w' {
		return time.Duration(n) * 7 * 24 * time.Hour, true
	}
	return time.Duration(n) * 24 * time.Hour, true
}

// fleetUsageRow is one aggregated (provider, model) row: the sum of every
// matching provider_usage day-bucket, plus the earliest/latest bucket the
// sum spans (full_desc's "date range covered").
type fleetUsageRow struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Requests     int64  `json:"requests"`
	TokensIn     int64  `json:"tokens_in"`
	TokensOut    int64  `json:"tokens_out"`
	CostMicroUSD int64  `json:"cost_micro_usd"`
	RangeStart   string `json:"range_start"`
	RangeEnd     string `json:"range_end"`
}

func (r fleetUsageRow) String() string {
	return fmt.Sprintf("%-16s %-24s requests=%-4d tokens_in=%-8d tokens_out=%-8d cost_micro_usd=%-8d range=%s..%s",
		r.Provider, r.Model, r.Requests, r.TokensIn, r.TokensOut, r.CostMicroUSD, r.RangeStart, r.RangeEnd)
}

// fleetUsageResult is `cascade fleet usage`'s --json/table payload.
type fleetUsageResult struct {
	Rows []fleetUsageRow `json:"rows"`
}

func (r fleetUsageResult) String() string {
	if len(r.Rows) == 0 {
		return "no usage recorded"
	}
	lines := make([]string, len(r.Rows))
	for i, row := range r.Rows {
		lines[i] = row.String()
	}
	return strings.Join(lines, "\n")
}

// aggregateFleetUsage groups filtered provider_usage rows by (provider,
// model), summing every counter across whichever day-buckets and lanes
// matched the filter, and tracking the bucket range (bucket strings are
// YYYY-MM-DD, so lexicographic min/max is chronological min/max). Output
// order is stable: provider then model, ascending.
func aggregateFleetUsage(rows []provider.UsageSummary) []fleetUsageRow {
	type key struct{ provider, model string }
	agg := make(map[key]*fleetUsageRow)
	var order []key
	for _, s := range rows {
		k := key{s.ProviderName, s.ModelName}
		r, ok := agg[k]
		if !ok {
			r = &fleetUsageRow{Provider: s.ProviderName, Model: s.ModelName, RangeStart: s.Bucket, RangeEnd: s.Bucket}
			agg[k] = r
			order = append(order, k)
		}
		r.Requests += s.Requests
		r.TokensIn += s.TokensIn
		r.TokensOut += s.TokensOut
		r.CostMicroUSD += s.CostMicroUSD
		if s.Bucket < r.RangeStart {
			r.RangeStart = s.Bucket
		}
		if s.Bucket > r.RangeEnd {
			r.RangeEnd = s.Bucket
		}
	}
	sort.Slice(order, func(i, j int) bool {
		if order[i].provider != order[j].provider {
			return order[i].provider < order[j].provider
		}
		return order[i].model < order[j].model
	})
	out := make([]fleetUsageRow, 0, len(order))
	for _, k := range order {
		out = append(out, *agg[k])
	}
	return out
}
