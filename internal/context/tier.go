package context

// Purpose: the five-level context-tier model (GCI/ASI/PPI/PRI/PAI) that
//   internal/context's discovery walk (discover.go) resolves against. This
//   file owns the types only; merge/precedence semantics over a resolved
//   []TierRecord are deferred to a later ticket (T2).
// Inputs: none — pure type definitions.
// Outputs: TierRole, its String()/Valid() helpers, and TierRecord.
// Constraints: 04-PEWS-PLAN-W1-W3.md Wave 2 Epic E S-08 T1; role names and
//   ordinals are fixed by the plan (GCI first / lowest ordinal, PAI last /
//   highest) and must not be renumbered without a plan amendment.
// SPORT: context-engine/tier-model (ADD, per T-1 sport_updates).

// TierRole identifies one of the five context-cascade tiers, in strict
// authority order: GCI (global, ~HOME) is the most general and highest
// authority; PAI (app, at or below the git root) is the most specific and
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
	// TierASI is the All-Sites Instructions tier: the directory two levels
	// above the git root (the repo's grandparent), conventionally the
	// root that holds every project a user works on. Applies to every
	// project under that root.
	TierASI
	// TierPPI is the Per-Project Instructions tier: the directory one
	// level above the git root (the repo's parent). Applies to every repo
	// that makes up one multi-repo project.
	TierPPI
	// TierPRI is the Per-Repo Instructions tier: the git root itself (or
	// the working directory, when it is not inside a git repository).
	// Applies to one repository.
	TierPRI
	// TierPAI is the Per-App Instructions tier: a directory strictly
	// below the git root — one app inside a multi-app repo. Present only
	// when the working directory is not the repo root itself.
	TierPAI
)

// tierRoleNames holds the display string for each valid TierRole, indexed
// by TierRole value. Index 0 is the invalid zero value's placeholder.
var tierRoleNames = [...]string{
	"",
	"GCI",
	"ASI",
	"PPI",
	"PRI",
	"PAI",
}

// String returns the tier's stable uppercase short name, e.g. "GCI" or
// "PAI". These strings are part of the tier's identity wherever it is
// logged or rendered; they must never change once shipped.
func (r TierRole) String() string {
	if !r.Valid() {
		return "invalid-tier"
	}
	return tierRoleNames[r]
}

// Valid reports whether r is one of the five defined tiers. The zero value
// and any value beyond TierPAI are invalid.
func (r TierRole) Valid() bool {
	return r >= TierGCI && r <= TierPAI
}

// allTierRoles returns the five tiers in ascending-ordinal order (GCI
// first, PAI last) — the same order Discover returns its []TierRecord in.
// Tests use it to assert the enumeration stays exactly five members.
func allTierRoles() []TierRole {
	return []TierRole{TierGCI, TierASI, TierPPI, TierPRI, TierPAI}
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
	// is 0; PAI is 4). It always equals the record's index in the slice
	// Discover returns.
	Ordinal int
	// Dir is the directory this tier resolves to, or "" when the tier has
	// no candidate directory at all (for example: ASI when the git root
	// sits too close to HOME for a distinct ASI directory to exist — see
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
}
