// Package repo implements the semantic repository inventory model (R-16.17,
// R-16.29): deterministic detection of a repository's languages,
// build/test/lint commands, layout, CI presence and existing harness
// files. No LLM anywhere in this package; every fact here is derived by a
// deterministic detector from an evidence file on disk.
package repo

// Purpose: the inventory model types persisted by store.go and produced
//   by scan.go: RepositoryRef (the R-16.3 repository record shape, read
//   from the existing context_repository/context_repo_path tables owned
//   by internal/context/scope -- this package defines no parallel
//   repository table), LanguageFacts (one detector family's result),
//   Commands, LayoutFacts, CIFacts, HarnessFacts and Inventory (the
//   aggregate this package persists).
// Constraints: no bare time.Now; Inventory.ScannedAt is set by the
//   caller from an injected clock. Every enum here is CLOSED and carries
//   a Valid() gate so a malformed or unrecognized value fails closed
//   rather than being silently accepted.
// SPORT: repo/inventory-model/ADD (P1-E33-W7-S67-T1).

// Language identifies one of the six detector families this package
// implements. The set is closed: LanguageUnknown is the explicit,
// never-guessed value a caller uses when no detector has run or no
// evidence file was found for a given family -- it is never returned by
// a Detector itself (a Detector either finds its evidence and returns a
// concrete Language with Detected=true, or reports Detected=false).
type Language string

// The closed Language vocabulary, one constant per detector family plus
// the LanguageUnknown fail-closed sentinel documented below.
const (
	LanguageGo      Language = "go"
	LanguageJSTS    Language = "jsts"
	LanguageRust    Language = "rust"
	LanguagePython  Language = "python"
	LanguageSwift   Language = "swift"
	LanguageGeneric Language = "generic"
	// LanguageUnknown is the explicit fail-closed sentinel: a repository
	// or file the registry could not classify. Never a guess, never
	// silently dropped from a result set.
	LanguageUnknown Language = "unknown"
)

// Valid reports whether l is one of the seven closed values above.
func (l Language) Valid() bool {
	switch l {
	case LanguageGo, LanguageJSTS, LanguageRust, LanguagePython, LanguageSwift, LanguageGeneric, LanguageUnknown:
		return true
	default:
		return false
	}
}

// Commands carries one language family's build/test/lint invocations.
// PackageManager is set only for families where evidence disambiguates it
// (js/ts: "pnpm" when a pnpm-lock.yaml is present); it is "" otherwise.
type Commands struct {
	Build   string
	Test    string
	Lint    string
	Package string
}

// LanguageFacts is one Detector's result for a repository root.
// Detected=false with a nil error means "this family's evidence file is
// absent" -- a clean, non-guessed absence, never an error. A non-nil
// error means the evidence file IS present but malformed; Detected and
// Commands are meaningless in that case and callers must not use them.
type LanguageFacts struct {
	Language Language
	Detected bool
	Commands Commands
	// Evidence lists the repo-relative marker file path(s) that produced
	// this result (e.g. "go.mod", "package.json", "pnpm-lock.yaml").
	// Never an absolute path (never a /Volumes or /Users path leaks into
	// a stored record).
	Evidence []string
}

// LayoutFacts summarizes the repository tree walk.go performs: directory/
// file counts and whether the walk hit its safety bound (see layout.go's
// walkBudget doc comment). Reaching the bound is not an error -- a huge
// tree is bounded, not refused -- but Truncated=true tells a caller the
// counts are a lower bound, not exact.
type LayoutFacts struct {
	DirCount  int
	FileCount int
	MaxDepth  int
	Truncated bool
	CaseClash []string // repo-relative paths that collide case-insensitively
}

// CIFacts records which CI system markers were found at the repo root.
type CIFacts struct {
	GitHubActions bool
	GitLabCI      bool
	CircleCI      bool
}

// HarnessFacts records which pre-existing AI-harness files/dirs were
// found at the repo root.
type HarnessFacts struct {
	ClaudeMD  bool
	AgentsMD  bool
	ClaudeDir bool
}

// RepositoryRef identifies the repository an inventory belongs to,
// mirroring the R-16.3 repository record shape (context_repository:
// id, remote, path_hash) that internal/context/scope already owns and
// persists. This package never writes that table; it only reads/carries
// the id as a foreign key into its own inventory row.
type RepositoryRef struct {
	ID       string
	Remote   string
	PathHash string
}

// MembershipRef names one scope-graph node (kind/id pair, matching
// internal/context/scope's Ref shape) the repository resolved as a
// member of. internal/repo stores this as read-only evidence of what the
// scope-graph resolver returned; it owns no scope/scope_edge rows itself.
type MembershipRef struct {
	Kind string
	ID   string
}

// Inventory is the aggregate this package persists: one row per
// RepositoryRef, keyed by RepositoryRef.ID.
type Inventory struct {
	Repository RepositoryRef
	Languages  []LanguageFacts
	Layout     LayoutFacts
	CI         CIFacts
	Harness    HarnessFacts
	Membership []MembershipRef
	// ScannedAt is a Unix-seconds timestamp from the caller's injected
	// clock (A-T4) -- never a bare time.Now() call in this package.
	ScannedAt int64
}
