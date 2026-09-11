package provider

// Purpose: ReviewProvider — the contract a plugin providing CR-A/B/C
//
//	review must implement, feeding Y/S-52.T4's native adversarial
//	reviewer. Declares its own request/response/finding types rather than
//	importing plugins/pbd/internal/pews's CRLevel vocabulary: pkg/provider
//	imports nothing from internal/ (Art.10.2), and plugins/pbd's review
//	tiers are that plugin's own internal detail, not a pkg/-layer type.
//
// Inputs: none at this layer — a contract, not behavior.
// Outputs: none.
// Constraints: pkg/provider imports nothing from internal/ (Art.10.2);
//
//	reuses Capabilities (types.go) for the compliance-posture descriptor
//	rather than declaring a parallel one.
//
// SPORT: pkg.provider.ReviewProvider/ADD (P1-E15-W4-S33-T1).

import "context"

// ReviewCRLevel identifies which review tier a ReviewRequest asks a
// ReviewProvider to perform: CR-A (light), CR-B (peer), or CR-C
// (architecture). The vocabulary matches the ticket-level cr_level field
// used across this repository's own phase tracking, restated here as a
// pkg/-layer type so a plugin implementing ReviewProvider needs no
// internal/ import to speak it.
type ReviewCRLevel string

// The closed set of ReviewCRLevel values.
const (
	// ReviewCRLevelA is a light, single-pass review.
	ReviewCRLevelA ReviewCRLevel = "CR-A"
	// ReviewCRLevelB is a peer review.
	ReviewCRLevelB ReviewCRLevel = "CR-B"
	// ReviewCRLevelC is an architecture-level review.
	ReviewCRLevelC ReviewCRLevel = "CR-C"
)

// Valid reports whether l is one of the three declared ReviewCRLevel
// members. The zero value (empty string) is not valid.
func (l ReviewCRLevel) Valid() bool {
	switch l {
	case ReviewCRLevelA, ReviewCRLevelB, ReviewCRLevelC:
		return true
	default:
		return false
	}
}

// ReviewRequest is the input to ReviewProvider.Review: the change under
// review, at the tier the caller is asking for.
type ReviewRequest struct {
	// Level is the requested review tier.
	Level ReviewCRLevel
	// Diff is the unified diff (or full file contents, for a new file)
	// under review.
	Diff string
	// Context is free-text background the caller supplies: the ticket or
	// task description, acceptance criteria, or anything else a reviewer
	// needs beyond the diff itself.
	Context string
}

// ReviewSeverity classifies one ReviewFinding by how strongly it should
// block the change under review.
type ReviewSeverity string

// The closed set of ReviewSeverity values, ordered least to most blocking.
const (
	// ReviewSeverityNit is a style or polish note that never blocks.
	ReviewSeverityNit ReviewSeverity = "nit"
	// ReviewSeverityMinor should be fixed but does not block on its own.
	ReviewSeverityMinor ReviewSeverity = "minor"
	// ReviewSeverityMajor should block unless explicitly waived.
	ReviewSeverityMajor ReviewSeverity = "major"
	// ReviewSeverityBlocker always blocks.
	ReviewSeverityBlocker ReviewSeverity = "blocker"
)

// Valid reports whether s is one of the four declared ReviewSeverity
// members.
func (s ReviewSeverity) Valid() bool {
	switch s {
	case ReviewSeverityNit, ReviewSeverityMinor, ReviewSeverityMajor, ReviewSeverityBlocker:
		return true
	default:
		return false
	}
}

// ReviewFinding is one issue a ReviewProvider raised against the reviewed
// change.
type ReviewFinding struct {
	// Severity classifies how strongly this finding should block.
	Severity ReviewSeverity
	// File is the path the finding concerns, relative to the repository
	// root. Empty when the finding is not tied to one file.
	File string
	// Line is the 1-based line number the finding concerns within File.
	// Zero when the finding is not tied to one line.
	Line int
	// Message is the human-readable explanation.
	Message string
}

// ReviewResponse is the result of ReviewProvider.Review: every finding, and
// whether the reviewer approves the change overall. Approved can be true
// alongside a non-empty Findings slice (e.g. only nits), and must be false
// whenever any finding is ReviewSeverityBlocker.
type ReviewResponse struct {
	// Findings lists every issue raised, in no particular order.
	Findings []ReviewFinding
	// Approved reports the reviewer's overall verdict.
	Approved bool
}

// ReviewProvider is the contract a plugin providing CR-A/B/C review must
// implement.
type ReviewProvider interface {
	// Review performs req.Level's review over req.Diff and returns every
	// finding plus an overall verdict.
	Review(ctx context.Context, req ReviewRequest) (ReviewResponse, error)
	// Capabilities describes the named lane's current tool-capability
	// support and compliance posture, matching AgentProvider and
	// ModelProvider's Capabilities shape.
	Capabilities(ctx context.Context, lane string) (Capabilities, error)
}
