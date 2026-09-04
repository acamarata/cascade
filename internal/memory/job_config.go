package memory

// Purpose: the [memory] configuration section — the keys this subsystem
//   reads, the shipped defaults an absent section resolves to, and the
//   strict parser that turns the raw table into a JobConfig. Split from
//   rpc_admin.go under Art.10.3's 300-line file cap when the review
//   digest's cadence key was added (P1-E07-W2-S14-T3).
// Inputs: the raw [memory] table as the config loader decoded it.
// Outputs: a fully-resolved JobConfig, or a typed pkg/cascade refusal
//   naming the fully dotted key.
// Constraints: strict about the keys it owns and silent about the ones it
//   does not; a wrong TYPE is a hard refusal rather than a silent
//   fallback, because a user who configured a value must never be told a
//   daemon started with a different one; no clock read here.
// SPORT: internal.memory.JobConfig (CHANGED, P1-E07-W2-S14-T3).

import (
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

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
	// ConfigKeyReviewCadenceDays is the review digest's cadence, in days.
	// It is ONE value rather than a schedule beside a window because the
	// digest reports on exactly the stretch of time since its previous
	// fire: two keys could be set to disagree, and a digest whose window
	// is shorter than its period would silently skip promotions.
	ConfigKeyReviewCadenceDays = "review_cadence_days"
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

// DefaultReviewCadenceDays is the review digest's period, in days. Weekly
// is what 07 calls the digest, and it is the only cadence at which a
// digest is worth reading rather than being noise a user learns to ignore.
const DefaultReviewCadenceDays = 7

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
	// ReviewDigestSchedule is the digest job's cron spec and
	// ReviewDigestCadence is the window it reports on. Both are DERIVED
	// from the one cadence key, so they cannot be configured to disagree.
	ReviewDigestSchedule string
	ReviewDigestCadence  time.Duration
}

// DefaultJobConfig returns the shipped configuration, which is what an
// absent [memory] section resolves to.
func DefaultJobConfig() JobConfig {
	return JobConfig{
		ConsolidationSchedule: DefaultConsolidationSchedule,
		StalenessSchedule:     DefaultStalenessSchedule,
		Staleness:             StalenessConfig{Window: DefaultStalenessWindow},
		ReviewDigestSchedule:  reviewSchedule(DefaultReviewCadenceDays),
		ReviewDigestCadence:   reviewCadence(DefaultReviewCadenceDays),
	}
}

// reviewCadence turns a number of days into the digest window.
func reviewCadence(days int) time.Duration {
	return time.Duration(days) * 24 * time.Hour
}

// reviewSchedule turns a number of days into the scheduler cron spec that
// fires exactly that often, in the scheduler's own vocabulary.
func reviewSchedule(days int) string {
	return "@every " + reviewCadence(days).String()
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
	if err := parseReviewCadenceKey(table, &out); err != nil {
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

// parseReviewCadenceKey reads review_cadence_days, refusing anything that
// is not a positive whole number of days. A zero or negative cadence is
// refused rather than read as "off": a user who wants no digest disables
// the job, and silently treating a typo as a disabled digest would leave
// them waiting for a report that is never coming.
func parseReviewCadenceKey(table map[string]any, out *JobConfig) error {
	v, present := table[ConfigKeyReviewCadenceDays]
	if !present {
		return nil
	}
	var days int
	switch n := v.(type) {
	case int64:
		days = int(n)
	case int:
		days = n
	default:
		return configTypeError(ConfigKeyReviewCadenceDays, "a positive whole number of days")
	}
	if days <= 0 {
		return configTypeError(ConfigKeyReviewCadenceDays, "a positive whole number of days")
	}
	out.ReviewDigestCadence = reviewCadence(days)
	out.ReviewDigestSchedule = reviewSchedule(days)
	return nil
}

// configTypeError builds the one refusal shape this parser returns, so
// every message reads the same way and names the fully dotted key.
func configTypeError(key, want string) error {
	return cascade.Newf(cascade.KindInvalidInput,
		"memory.%s must be %s", key, want)
}
