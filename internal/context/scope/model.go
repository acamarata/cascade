package scope

import (
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the R-16.3 exported types: SessionScope's exact twelve fields,
//   the persisted-graph records, the closed edge-kind vocabulary, and the
//   ScopeKind enumeration the R-21.157 traversal table is keyed on.
// Inputs: none (types only); EdgeKind.Valid and ScopeKind.Valid are the
//   package's fail-closed vocabulary gates.
// Outputs: none.
// Constraints: SessionScope carries EXACTLY the twelve named fields R-16.3
//   lists — no additional field, no mutation grammar, no override-
//   precedence rule (R-16.3 defines none, so this ticket invents none).
// SPORT: context/scope-model/ADD.

// ScopeKind is the closed set of scope kinds the R-21.157 traversal table
// is keyed on, plus the `general` restricted-result kind R-16.3 defines
// for an unresolved cwd. ScopeKind is distinct from EdgeClass: a kind
// names WHAT a scope IS, an EdgeClass names HOW two scopes are connected.
type ScopeKind string

// The closed ScopeKind vocabulary. General is not one of the six
// traversal-table kinds (R-21.157 enumerates session|task|project|
// workspace|product|global) — a general-kind SessionScope has no graph
// membership to traverse from at all, by construction.
const (
	ScopeKindSession   ScopeKind = "session"
	ScopeKindTask      ScopeKind = "task"
	ScopeKindProject   ScopeKind = "project"
	ScopeKindWorkspace ScopeKind = "workspace"
	ScopeKindProduct   ScopeKind = "product"
	ScopeKindGlobal    ScopeKind = "global"
	// ScopeKindGeneral is the R-16.3 restricted successful result for an
	// unresolved cwd: user tiers plus cascade-pa context only, never a
	// fallback to an unscoped/global query.
	ScopeKindGeneral ScopeKind = "general"
)

// Valid reports whether k is one of the seven closed ScopeKind values.
func (k ScopeKind) Valid() bool {
	switch k {
	case ScopeKindSession, ScopeKindTask, ScopeKindProject, ScopeKindWorkspace,
		ScopeKindProduct, ScopeKindGlobal, ScopeKindGeneral:
		return true
	}
	return false
}

// EdgeKind is the closed set of persisted relationship values R-16.3
// declares as explicit data: "depends_on . member_of . shares_context_with
// (declared at init or by the user)". An unknown value fails with an A-T7
// typed invalid-input error (never a silent default).
type EdgeKind string

const (
	EdgeKindDependsOn         EdgeKind = "depends_on"
	EdgeKindMemberOf          EdgeKind = "member_of"
	EdgeKindSharesContextWith EdgeKind = "shares_context_with"
)

// Valid reports whether k is one of the three closed EdgeKind values.
func (k EdgeKind) Valid() bool {
	switch k {
	case EdgeKindDependsOn, EdgeKindMemberOf, EdgeKindSharesContextWith:
		return true
	}
	return false
}

// ValidateEdgeKind returns an A-T7 typed invalid-input error for any value
// outside the closed EdgeKind vocabulary, and nil for a valid one. Callers
// (PutEdge, the RPC decode path) call this before persisting or trusting a
// caller-supplied edge kind — the fail-closed gate R-16.3's contract text
// requires.
func ValidateEdgeKind(k EdgeKind) error {
	if !k.Valid() {
		return cascade.Newf(cascade.KindInvalidInput,
			"context/scope: unknown edge kind %q: must be one of depends_on, member_of, shares_context_with", string(k))
	}
	return nil
}

// ScopeRef identifies one node in the persisted scope graph: a kind plus
// the stable identifier of the record that kind resolves to (a repository
// remote+path hash for ScopeKindProject-adjacent records, a caller-
// supplied id for task/session). ScopeRef is comparable so it can be used
// as a map key for deduplication (CandidateScopeRefs).
type ScopeRef struct {
	Kind ScopeKind
	ID   string
}

// SessionScope is the R-16.3 resolved scope record. It carries EXACTLY
// these twelve fields — no more, no less, per the ticket's acceptance
// criteria — plus the Kind discriminator (`general` vs a resolved chain)
// that ResolveSessionScope's contract requires callers be able to branch
// on.
type SessionScope struct {
	// Kind is ScopeKindGeneral for an unresolved cwd, or the resolved
	// scope's own kind otherwise (ScopeKindSession once a session id is
	// attached, per the fixed resolution order).
	Kind ScopeKind `json:"kind"`

	User              string            `json:"user"`
	Machine           string            `json:"machine"`
	Cwd               string            `json:"cwd"`
	Workspace         string            `json:"workspace"`
	Product           string            `json:"product"`
	Project           string            `json:"project"`
	Repository        *RepositoryRecord `json:"repository"`
	PackagePath       string            `json:"package_path"`
	Branch            string            `json:"branch"`
	Task              string            `json:"task"`
	Session           string            `json:"session"`
	ExplicitOverrides string            `json:"explicit_overrides"`
}

// RepositoryRecord is the `repository` table's row shape: a git root
// identified by its remote URL plus a path hash (R-16.3: "repository
// record (remote URL + path hash)"), so the same clone at two different
// local paths, or two different clones of the same remote, are both
// addressable without ambiguity.
type RepositoryRecord struct {
	ID       string `json:"id"`
	RootPath string `json:"root_path"`
	Remote   string `json:"remote"`
	PathHash string `json:"path_hash"`
}

// RepoPathRecord is the `repo_path` table's row shape: one local
// filesystem anchor (the S-08.T1 git-root anchor value) bound to a
// RepositoryRecord, so RepositoryForRoot can look a resolved git root up
// directly without re-hashing on every call.
type RepoPathRecord struct {
	RepositoryID string
	RootPath     string
}

// ScopeGraphRecord is the `scope` table's row shape: one node in the
// persisted graph, addressed by ScopeRef, with an optional display name
// (never used for matching — R-16.3 forbids cross-project name matching).
type ScopeGraphRecord struct {
	Ref         ScopeRef
	DisplayName string
}

// ScopeEdge is the `scope_edge` table's row shape: one explicit,
// user/init-declared relationship between two scope nodes. Kind is
// validated against EdgeKind's closed vocabulary before it is ever
// persisted (store.go's PutEdge).
type ScopeEdge struct {
	From ScopeRef
	To   ScopeRef
	Kind EdgeKind
}
