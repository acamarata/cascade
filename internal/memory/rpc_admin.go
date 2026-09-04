package memory

// Purpose: memory.consolidate — the one-shot manual trigger for the
//   consolidation job, and the [memory] config section the scheduled job
//   and this verb are both configured from.
// Inputs: raw JSON params from an untrusted peer; the raw [memory] table
//   as the config loader decoded it.
// Outputs: a ConsolidationReport, or a pkg/cascade taxonomy error.
// Constraints: params decode into a concrete struct, never interface{};
//   every refusal is a taxonomy error; no clock read outside the injected
//   Clock the Consolidator already holds; no platform-specific imports.
// SPORT: internal.memory.rpc_admin.Handler (ADD, P1-E07-W2-S13-T4).

import (
	"context"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// MethodConsolidate runs the consolidation job once and returns its
// report. It is a constant because the daemon registers it and the CLI
// calls it by the same name.
const MethodConsolidate = "memory.consolidate"

// ConsolidateParams is memory.consolidate's input.
type ConsolidateParams struct {
	// DryRun asks what a consolidation would merge without merging it.
	// This verb retires a user's own records with no prompt anywhere in
	// the shipped path (§5.8 forbids one), so the rehearsal is the only
	// way to look before leaping.
	DryRun bool `json:"dry_run,omitempty"`
}

// AdminHandler serves the administrative half of the memory.* namespace:
// the jobs a user or the scheduler triggers, as opposed to the per-record
// verbs Handler serves.
//
// It is a separate type from Handler rather than more methods on it
// because it needs a different collaborator — the Consolidator, which owns
// the store TREE (the record files plus the consolidation records beside
// them), where Handler needs only the MemoryStore contract.
type AdminHandler struct {
	consolidator *Consolidator
	// cfg is the [memory] configuration this daemon resolved at startup.
	// The verb does not re-read config: a manual run must consolidate by
	// the same rules the background job does, and reading configuration
	// twice is how those two drift apart.
	cfg ConsolidationConfig
}

// NewAdminHandler returns an AdminHandler over c, running with cfg. cfg's
// DryRun is ignored — the per-call parameter decides that — and only its
// EmbeddingEnabled flag carries through.
func NewAdminHandler(c *Consolidator, cfg ConsolidationConfig) *AdminHandler {
	cfg.DryRun = false
	return &AdminHandler{consolidator: c, cfg: cfg}
}

// Register binds memory.consolidate on r. This is the whole of the
// composition-root wiring: without this call the handler is built, tested
// and unreachable from a running daemon.
func (h *AdminHandler) Register(r *rpc.Registry) {
	r.Register(MethodConsolidate, h.Consolidate)
}

// Compile-time proof that the method still satisfies the router's handler
// signature, so a drifting signature fails the build here rather than at
// the composition root.
var _ rpc.HandlerFunc = (*AdminHandler)(nil).Consolidate

// Consolidate serves memory.consolidate.
//
// # What it does
//
// It runs exactly one consolidation pass (see
// Consolidator.ConsolidateMemories for the algorithm and the crash
// contract) and returns its report. With DryRun it runs the whole grouping
// pass and reports what it WOULD retire, writing nothing and emitting
// nothing.
//
// # The advisory lock
//
// Exclusion between daemons is the scheduler's advisory lock (C/S-04.T4):
// only the lock holder ticks, so only one daemon ever runs the scheduled
// job. Within one process, this verb and the scheduled job share a
// Consolidator and its re-entrancy guard — a call that arrives while a run
// is in flight returns ConsolidationReport{Skipped:true} and a nil error
// rather than queueing behind a background job or racing it.
//
// # Errors
//
// KindInvalidInput for malformed params; KindUnsupported when
// [memory].consolidation_embedding is on, which this build cannot serve;
// KindIntegrity for a damaged consolidation record; KindUnavailable when
// the file system fails.
func (h *AdminHandler) Consolidate(ctx context.Context, params json.RawMessage) (any, error) {
	var p ConsolidateParams
	if err := decodeParams(MethodConsolidate, params, &p); err != nil {
		return nil, err
	}
	cfg := h.cfg
	cfg.DryRun = p.DryRun
	report, err := h.consolidator.ConsolidateMemories(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return report, nil
}

// The [memory] config keys this subsystem reads. They are named here, in
// the package that gives them meaning, so the composition root does not
// spell them as literals.
const (
	// ConfigKeyConsolidationSchedule is the cron spec of the consolidation
	// job.
	ConfigKeyConsolidationSchedule = "consolidation_schedule"
	// ConfigKeyStalenessSchedule is the cron spec of the staleness scan.
	ConfigKeyStalenessSchedule = "staleness_schedule"
	// ConfigKeyStalenessWindowDays is the staleness window, in days.
	ConfigKeyStalenessWindowDays = "staleness_window_days"
	// ConfigKeyConsolidationEmbedding is the default-off embedding
	// clustering flag.
	ConfigKeyConsolidationEmbedding = "consolidation_embedding"
)

// The default schedules for the two background jobs. Both are daily: the
// work is cheap (a tree walk and a hash per record), and a job that runs
// less often than the queue it feeds is reviewed would make the queue tell
// a user something that stopped being true weeks ago.
const (
	// DefaultConsolidationSchedule is the consolidation job's cron spec.
	DefaultConsolidationSchedule = "@every 24h0m0s"
	// DefaultStalenessSchedule is the staleness scan's cron spec.
	DefaultStalenessSchedule = "@every 24h0m0s"
)

// JobConfig is the resolved [memory] section: everything the two
// background jobs and the memory.consolidate verb need, with the shipped
// defaults already applied.
type JobConfig struct {
	// ConsolidationSchedule and StalenessSchedule are cron specs the
	// scheduler parses. They are passed through verbatim; this package
	// does not parse them, so a bad spec is refused by the scheduler with
	// the scheduler's own error rather than by a second parser here that
	// could disagree with it.
	ConsolidationSchedule string
	StalenessSchedule     string
	// Consolidation is the consolidation job's configuration.
	Consolidation ConsolidationConfig
	// Staleness is the staleness scan's configuration.
	Staleness StalenessConfig
}

// DefaultJobConfig returns the shipped configuration, which is what an
// absent [memory] section resolves to.
func DefaultJobConfig() JobConfig {
	return JobConfig{
		ConsolidationSchedule: DefaultConsolidationSchedule,
		StalenessSchedule:     DefaultStalenessSchedule,
		Staleness:             StalenessConfig{Window: DefaultStalenessWindow},
	}
}

// ParseJobConfig reads the raw [memory] table the config loader decoded.
//
// It is strict about the keys it owns and silent about the ones it does
// not: a wrong TYPE on one of this subsystem's keys is a hard refusal,
// because a user who wrote staleness_window_days = "30" configured a
// window and must not be told the daemon started with a different one.
// Keys this subsystem does not own (memory.review_cadence, S-14's) are
// passed over untouched rather than refused, since they belong to another
// ticket's parser reading the same table.
//
// A nil or absent table resolves to DefaultJobConfig with no error.
func ParseJobConfig(raw any) (JobConfig, error) {
	out := DefaultJobConfig()
	if raw == nil {
		return out, nil
	}
	table, ok := raw.(map[string]any)
	if !ok {
		return out, cascade.Newf(cascade.KindInvalidInput, "[memory] must be a table")
	}
	if err := parseScheduleKeys(table, &out); err != nil {
		return DefaultJobConfig(), err
	}
	if err := parseWindowKey(table, &out); err != nil {
		return DefaultJobConfig(), err
	}
	if v, present := table[ConfigKeyConsolidationEmbedding]; present {
		enabled, isBool := v.(bool)
		if !isBool {
			return DefaultJobConfig(), configTypeError(ConfigKeyConsolidationEmbedding, "a boolean")
		}
		out.Consolidation.EmbeddingEnabled = enabled
	}
	return out, nil
}

// parseScheduleKeys reads the two cron-spec keys.
func parseScheduleKeys(table map[string]any, out *JobConfig) error {
	for _, spec := range []struct {
		key string
		dst *string
	}{
		{ConfigKeyConsolidationSchedule, &out.ConsolidationSchedule},
		{ConfigKeyStalenessSchedule, &out.StalenessSchedule},
	} {
		v, present := table[spec.key]
		if !present {
			continue
		}
		s, isString := v.(string)
		if !isString || s == "" {
			return configTypeError(spec.key, "a non-empty cron spec string")
		}
		*spec.dst = s
	}
	return nil
}

// parseWindowKey reads staleness_window_days, accepting the integer TOML
// decodes a bare number into as well as a float, and refusing a value that
// is not a positive number of days.
func parseWindowKey(table map[string]any, out *JobConfig) error {
	v, present := table[ConfigKeyStalenessWindowDays]
	if !present {
		return nil
	}
	var days float64
	switch n := v.(type) {
	case int64:
		days = float64(n)
	case int:
		days = float64(n)
	case float64:
		days = n
	default:
		return configTypeError(ConfigKeyStalenessWindowDays, "a positive number of days")
	}
	if days <= 0 {
		return configTypeError(ConfigKeyStalenessWindowDays, "a positive number of days")
	}
	out.Staleness.Window = time.Duration(days * float64(24*time.Hour))
	return nil
}

// configTypeError builds the one refusal shape this parser returns, so
// every message reads the same way and names the fully dotted key.
func configTypeError(key, want string) error {
	return cascade.Newf(cascade.KindInvalidInput,
		"memory.%s must be %s", key, want)
}
