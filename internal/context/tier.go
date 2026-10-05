package context

// Purpose: the five-level context-tier model (GCI/APC/PPC/PRC/PAC) that
//   internal/context's discovery walk (discover.go) resolves against. This
//   file owns the types only; merge/precedence semantics over a resolved
//   []TierRecord are deferred to a later ticket (T2).
// Inputs: none — pure type definitions.
// Outputs: TierRole, its String()/Valid() helpers, and TierRecord.
// Constraints: 04-PEWS-PLAN-W1-W3.md Wave 2 Epic E S-08 T1; role names and
//   ordinals are fixed by the plan (GCI first / lowest ordinal, PAC last /
//   highest) and must not be renumbered without a plan amendment.
// SPORT: context-engine/tier-model (ADD, per T-1 sport_updates).

// TierRole identifies one of the five context-cascade tiers, in strict
// authority order: GCI (global, ~HOME) is the most general and highest
// authority; PAC (app, below the git root) is the most specific and
// lowest authority. A lower tier's instructions are meant to add context
// without contradicting a higher tier's — TierRole itself does not enforce
// that; it only names the position.
//
// The zero value is intentionally invalid, matching pkg/cascade.Kind's
// convention: a forgotten TierRole field reads as a bug, not as GCI.
type TierRole uint8

const (
	_ TierRole = iota // 0 is deliberately not a valid TierRole

	// TierGCI is the Global Cascade Instructions tier, anchored at the
	// user's home directory. Applies to every piece of work on the
	// machine.
	TierGCI
	// TierAPC is the All-Projects Context tier: the directory two levels
	// above the git root (the repo's grandparent), conventionally the
	// root that holds every project a user works on. Applies to every
	// project under that root.
	TierAPC
	// TierPPC is the Per-Project Context tier: the directory one
	// level above the git root (the repo's parent). Applies to every repo
	// that makes up one multi-repo project.
	TierPPC
	// TierPRC is the Per-Repo Context tier: the git root itself (or
	// the working directory, when it is not inside a git repository).
	// Applies to one repository.
	TierPRC
	// TierPAC is the Per-App Context tier: every directory strictly
	// below the git root that holds a tier file, nearest to the working
	// directory last. All of them carry the PAC role; the working directory
	// itself is always represented (Absent when it is the repo root).
	TierPAC
)

// tierRoleNames holds the display string for each valid TierRole, indexed
// by TierRole value. Index 0 is the invalid zero value's placeholder.
var tierRoleNames = [...]string{
	"",
	"GCI",
	"APC",
	"PPC",
	"PRC",
	"PAC",
}

// String returns the tier's stable uppercase short name, e.g. "GCI" or
// "PAC". These strings are part of the tier's identity wherever it is
// logged or rendered; they must never change once shipped.
func (r TierRole) String() string {
	if !r.Valid() {
		return "invalid-tier"
	}
	return tierRoleNames[r]
}

// Valid reports whether r is one of the five defined tiers. The zero value
// and any value beyond TierPAC are invalid.
func (r TierRole) Valid() bool {
	return r >= TierGCI && r <= TierPAC
}

// DiscoverFinding names a non-fatal condition Discover met while resolving
// tiers. Findings ride on a TierRecord; they never abort discovery.
type DiscoverFinding string

// Findings a TierRecord can carry.
const (
	// FindingTierTooLarge marks a tier file larger than maxTierBytes, which is
	// treated as Absent and never read.
	FindingTierTooLarge DiscoverFinding = "tier_too_large"
	// FindingGitUnavailable marks a git invocation that failed for a reason
	// other than "not a repository" (git missing, exec failure).
	FindingGitUnavailable DiscoverFinding = "git_unavailable"
	// FindingGitPermission marks a git invocation refused for permissions.
	FindingGitPermission DiscoverFinding = "git_permission"
)

// allTierRoles returns the five tiers in ascending-ordinal order (GCI
// first, PAC last) — the same order Discover returns its []TierRecord in.
// Tests use it to assert the enumeration stays exactly five members.
func allTierRoles() []TierRole {
	return []TierRole{TierGCI, TierAPC, TierPPC, TierPRC, TierPAC}
}

// TierRecord is one resolved (or absent) tier produced by Discover.
//
// A TierRecord always carries a valid Role and Ordinal, even when Absent is
// true: absence is a property of the tier's content, not of the record.
type TierRecord struct {
	// Role is the tier this record represents.
	Role TierRole
	// Ordinal is the record's position in precedence order: lower ordinal
	// means further from the working directory and higher authority (GCI
	// is 0; PAC is 4). It always equals the record's index in the slice
	// Discover returns.
	Ordinal int
	// Dir is the directory this tier resolves to, or "" when the tier has
	// no candidate directory at all (for example: APC when the git root
	// sits too close to HOME for a distinct APC directory to exist — see
	// discover.go's boundary guard). Dir may be set even when Absent is
	// true: the directory exists, but it has no tier instruction file.
	Dir string
	// Path is the tier instruction file's full path (Dir plus the fixed
	// tier-file layout), or "" when Dir is "".
	Path string
	// Content is the tier instruction file's raw bytes, as a string, or ""
	// when Absent is true.
	Content string
	// Absent reports that this tier has no instruction file to read —
	// either because Dir is "" (no candidate directory) or because the
	// candidate directory exists but its tier file does not. Absence is
	// never an error: callers decide how to treat a missing tier.
	Absent bool
	// Findings lists non-fatal conditions met while resolving this record
	// (nil when none).
	Findings []DiscoverFinding
}
