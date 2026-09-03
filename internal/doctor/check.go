package doctor

import (
	"context"
	"errors"
)

// Purpose: the Check interface and its value types — the contract every
//
//	subsystem implements to plug a diagnostic into `cascade doctor`.
//
// Inputs: none at this layer; Check implementations receive a
//
//	context.Context per call (Run/Fix), carrying the per-check deadline
//	the Runner (runner.go) sets.
//
// Outputs: CheckResult (Run) and FixResult (Fix) value types.
// Constraints: CheckResult/FixResult are value types (no pointer
//
//	receivers/fields) per the ticket contract; Fix on a non-fixable
//	check must return ErrCheckNotFixable, never a silently-ignored nil.
//
// SPORT: placeholder: doctor/framework (ADD).

// Status is a check's outcome tier. Only the three tiers below exist —
// exhaustive is enabled in .golangci.yml specifically so a future switch
// over Status cannot silently drop a case.
type Status string

const (
	// StatusOK reports the check found nothing wrong.
	StatusOK Status = "ok"
	// StatusWarn reports a non-fatal issue: the system is usable but a
	// remediation is recommended.
	StatusWarn Status = "warn"
	// StatusError reports a fatal issue, or that the check could not
	// verify its subject at all (Art.1 — an unverifiable subject is
	// reported as an error, never a silent OK; see CheckResult.Message
	// doc comment).
	StatusError Status = "error"
)

// CheckMeta is a check's static metadata, read by the Runner before any
// Run/Fix call.
type CheckMeta struct {
	// FirstRun tags a check as relevant to `cascade doctor --first-run`
	// (08-INIT-CONFIG-SPEC §1 step 9). A check with FirstRun=false is
	// still run by a plain `cascade doctor`; --first-run filters TO
	// FirstRun=true checks only.
	FirstRun bool
	// Fixable declares whether Fix is meaningfully implemented. A check
	// with Fixable=false must have Fix return ErrCheckNotFixable
	// unconditionally — the Runner never calls Fix on a check that
	// declares Fixable=false, but Fix itself stays total and correct if
	// called directly by a test.
	Fixable bool
}

// CheckResult is one check's Run outcome.
//
// Art.1: a check that cannot actually verify its subject (a missing
// dependency, an unreachable resource, a platform it does not support)
// must set Status to StatusError with Message explaining WHY verification
// was not possible — never StatusOK. A green tick from a probe that
// silently no-opped is worse than no doctor at all.
type CheckResult struct {
	// Status is the check's outcome tier.
	Status Status
	// Message is a short, human-readable one-liner (surfaced in the TTY
	// summary table and the --json envelope's data).
	Message string
	// Detail is optional longer-form context (the specific query/probe
	// that failed, a raw error string). Never included in the TTY
	// summary table's default row; shown on --json or a verbose render.
	Detail string
	// Remediation is a human-readable hint for how to fix the issue
	// manually, shown even when the check itself is not Fixable.
	Remediation string
}

// FixResult is one check's Fix outcome.
type FixResult struct {
	// Applied is true when Fix actually mutated something. A second
	// --fix run on an already-correct system must report Applied=false
	// with an empty Delta (idempotency AC, plan §5.9) — Fix observing
	// "already correct" is success, not a no-op error.
	Applied bool
	// Delta is a human-readable description of what changed. Empty when
	// Applied is false.
	Delta string
}

// ErrCheckNotFixable is the sentinel Fix returns for a check whose
// Metadata().Fixable is false. Callers compare with errors.Is.
var ErrCheckNotFixable = errors.New("doctor: check is not fixable")

// Check is the diagnostic unit every subsystem registers with a
// CheckRegistry. Implementations must be safe to construct at
// composition-root init time (Name/Describe/Metadata must not block or
// error) — only Run/Fix do real work.
type Check interface {
	// Name is a stable, slug-shaped identifier ([a-z0-9_-]+, no spaces).
	// It is the key checks are registered and looked up under, and the
	// row label in every rendered report.
	Name() string
	// Describe is a human-readable one-liner shown alongside Name in the
	// TTY summary and `cascade doctor` listings.
	Describe() string
	// Run executes the check against ctx's deadline (set by the Runner
	// per-check; see runner.go). Run must return promptly once ctx is
	// done rather than relying on the Runner's panic/timeout recovery as
	// its normal exit path.
	Run(ctx context.Context) (CheckResult, error)
	// Fix attempts to remediate an issue Run previously reported. Called
	// only when --fix is passed and the prior Run returned warn/error
	// and Metadata().Fixable is true. A non-fixable check returns
	// ErrCheckNotFixable and a zero FixResult.
	Fix(ctx context.Context) (FixResult, error)
	// Metadata returns the check's static tags.
	Metadata() CheckMeta
}
