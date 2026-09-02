// Purpose: ParseSpec — the CronJob.Spec grammar this package accepts —
//   plus Schedule.NextAfter, the skip-missed occurrence calculator task 3
//   depends on. Split from job.go as a sibling file under R-14.117's
//   authorized-split allowance (Art.10.3's 300-line cap; mechanical
//   relocation, no behavior change — job.go and this file together are
//   the one T-4 CronJob/schedule unit).
// Inputs: an arbitrary caller-supplied Spec string (including hostile/
//   malformed input — FuzzCronParse in fuzz_test.go proves ParseSpec and
//   NextAfter never panic on any string).
// Outputs: a Schedule (whose NextAfter computes the next matching
//   instant), or a cascade.KindInvalidInput/KindUnsupported error.
// Constraints: no bare time.Now (R-14.11) — NextAfter's "now" is always
//   the caller-supplied `from` argument, sourced by scheduler.go from the
//   injected Clock.
// SPORT: internal.events.scheduler.ParseSpec/ADDED,
//   internal.events.scheduler.Schedule/ADDED (P1-E03-W1-S04-T4).

package scheduler

import (
	"strconv"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Schedule computes the occurrences of a parsed CronJob.Spec.
type Schedule interface {
	// NextAfter returns the earliest instant strictly after from that
	// satisfies the schedule, or a cascade.KindUnsupported error if none
	// is found within the search bound (nextSearchBound).
	NextAfter(from time.Time) (time.Time, error)
}

// nextSearchBound caps how far into the future a standard cron field's
// brute-force search looks before giving up — long enough that any
// satisfiable 5-field combination (including a leap-day-only spec) is
// found, short enough that an unsatisfiable one (e.g. day-of-month 31 AND
// month February, which never co-occurs) fails fast rather than hanging.
const nextSearchBound = 5 * 365 * 24 * time.Hour

// ParseSpec parses spec into a Schedule. Two forms are accepted:
//
//   - "@every <duration>", where <duration> is anything time.ParseDuration
//     accepts (e.g. "168h", "90m"). This is the form
//     RegisterRetentionJobs uses for the weekly (168h) retention default.
//   - a standard 5 whitespace-separated numeric field cron expression,
//     "minute hour day-of-month month day-of-week", each field one of:
//     "*" (any), an integer, a comma-separated list, a dash range "a-b",
//     or a step "*/n" or "a-b/n". Named months/weekdays (JAN, MON, ...)
//     are NOT accepted — this is a deliberately small internal dialect,
//     not a drop-in cron(5) implementation; see field ranges below.
//
// Field ranges: minute 0-59, hour 0-23, day-of-month 1-31, month 1-12,
// day-of-week 0-6 (0 = Sunday). A match requires ALL FIVE fields to match
// (AND semantics) — this package deliberately does NOT implement POSIX
// cron's day-of-month/day-of-week OR special case, which is a common
// source of surprise even in POSIX-conformant tools; every consumer in
// this repo (retention's "@every 168h") uses the interval form, so the
// simpler, unambiguous AND semantics was chosen over exact cron(5) parity.
//
// ParseSpec never panics for any input string, including empty, malformed,
// or adversarial ones (FuzzCronParse) — it returns a
// cascade.KindInvalidInput error instead.
func ParseSpec(spec string) (Schedule, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "scheduler: empty cron spec")
	}
	if rest, ok := strings.CutPrefix(trimmed, "@every"); ok {
		return parseEverySpec(rest)
	}
	return parseCronFields(trimmed)
}

// parseEverySpec parses the remainder of an "@every <duration>" spec
// (everything after the "@every" token).
func parseEverySpec(rest string) (Schedule, error) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "scheduler: @every requires a duration")
	}
	d, err := time.ParseDuration(rest)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, err, "scheduler: invalid @every duration %q", rest)
	}
	if d <= 0 {
		return nil, cascade.Newf(cascade.KindInvalidInput, "scheduler: @every duration %q must be positive", rest)
	}
	return everySchedule{interval: d}, nil
}

// everySchedule is a fixed-interval Schedule: every occurrence is exactly
// interval after the previous one, computed from `from` (never from an
// anchor epoch), which is what makes catch-up storms structurally
// impossible — NextAfter never looks backward.
type everySchedule struct {
	interval time.Duration
}

func (s everySchedule) NextAfter(from time.Time) (time.Time, error) {
	return from.Add(s.interval), nil
}

// cronFields is a parsed standard 5-field cron Schedule.
type cronFields struct {
	minute, hour, dom, month, dow fieldSet
}

// fieldSet is the set of integer values one cron field accepts, expanded
// at parse time (never lazily) so NextAfter's per-minute match check is a
// plain map lookup.
type fieldSet map[int]bool

func (c cronFields) matches(t time.Time) bool {
	return c.minute[t.Minute()] &&
		c.hour[t.Hour()] &&
		c.dom[t.Day()] &&
		c.month[int(t.Month())] &&
		c.dow[int(t.Weekday())]
}

func (c cronFields) NextAfter(from time.Time) (time.Time, error) {
	// Start the search at the next whole minute strictly after `from`
	// (cron granularity is one minute; seconds/nanoseconds are truncated).
	t := from.UTC().Truncate(time.Minute).Add(time.Minute)
	limit := from.UTC().Add(nextSearchBound)
	for !t.After(limit) {
		if c.matches(t) {
			return t, nil
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, cascade.Newf(cascade.KindUnsupported,
		"scheduler: cron spec matches no instant within %s of %s", nextSearchBound, from.UTC().Format(time.RFC3339))
}

// cronFieldRanges gives each of the 5 standard fields' (min, max) bound, in
// field order.
var cronFieldRanges = [5][2]int{
	{0, 59}, // minute
	{0, 23}, // hour
	{1, 31}, // day-of-month
	{1, 12}, // month
	{0, 6},  // day-of-week
}

// parseCronFields parses a standard 5-field cron expression.
func parseCronFields(spec string) (Schedule, error) {
	tokens := strings.Fields(spec)
	if len(tokens) != 5 {
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"scheduler: cron spec must have exactly 5 fields, got %d", len(tokens))
	}
	sets := make([]fieldSet, 5)
	for i, tok := range tokens {
		set, err := parseField(tok, cronFieldRanges[i][0], cronFieldRanges[i][1])
		if err != nil {
			return nil, err
		}
		sets[i] = set
	}
	return cronFields{minute: sets[0], hour: sets[1], dom: sets[2], month: sets[3], dow: sets[4]}, nil
}

// parseField parses one cron field (a comma-separated list of "*", "*/n",
// "a", "a-b", or "a-b/n" tokens, each within [min,max]) into the set of
// integers it matches.
func parseField(field string, minVal, maxVal int) (fieldSet, error) {
	if field == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "scheduler: empty cron field")
	}
	set := fieldSet{}
	for _, part := range strings.Split(field, ",") {
		if err := parseFieldPart(part, minVal, maxVal, set); err != nil {
			return nil, err
		}
	}
	return set, nil
}

// parseFieldPart parses one comma-separated token of a cron field into set.
func parseFieldPart(part string, minVal, maxVal int, set fieldSet) error {
	base, step, err := splitStep(part)
	if err != nil {
		return err
	}
	lo, hi, err := parseRange(base, minVal, maxVal)
	if err != nil {
		return err
	}
	for v := lo; v <= hi; v += step {
		set[v] = true
	}
	return nil
}

// splitStep splits "<base>/<step>" into its base range/wildcard token and
// step (default 1 when there is no "/step" suffix).
func splitStep(part string) (base string, step int, err error) {
	base, stepStr, hasStep := strings.Cut(part, "/")
	if !hasStep {
		return base, 1, nil
	}
	step, serr := strconv.Atoi(stepStr)
	if serr != nil || step <= 0 {
		return "", 0, cascade.Newf(cascade.KindInvalidInput, "scheduler: invalid cron step %q", stepStr)
	}
	return base, step, nil
}

// parseRange parses "*", a single integer, or "a-b" into an inclusive
// [lo,hi] bound, validated against [min,max].
func parseRange(base string, minVal, maxVal int) (lo, hi int, err error) {
	if base == "*" {
		return minVal, maxVal, nil
	}
	loStr, hiStr, isRange := strings.Cut(base, "-")
	lo, err = strconv.Atoi(loStr)
	if err != nil {
		return 0, 0, cascade.Newf(cascade.KindInvalidInput, "scheduler: invalid cron value %q", loStr)
	}
	if isRange {
		hi, err = strconv.Atoi(hiStr)
		if err != nil {
			return 0, 0, cascade.Newf(cascade.KindInvalidInput, "scheduler: invalid cron value %q", hiStr)
		}
	} else {
		hi = lo
	}
	if lo < minVal || hi > maxVal || lo > hi {
		return 0, 0, cascade.Newf(cascade.KindInvalidInput,
			"scheduler: cron value(s) %d-%d out of range [%d,%d]", lo, hi, minVal, maxVal)
	}
	return lo, hi, nil
}
