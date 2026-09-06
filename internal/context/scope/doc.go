// Package scope implements the R-16.3 session-scope model: SessionScope
// resolution from a git-root anchor plus the persisted context-domain
// scope graph (the four tables scope, scope_edge, repository, repo_path),
// the R-21.157 closed traversal table, and deny-by-default candidate
// selection.
//
// Purpose: answer "what scope is this session in, and what other scopes
//
//	may it see" without ever inventing an implicit relationship or
//	widening authorization — every visible scope traces to either the
//	session's own resolved chain or an explicitly persisted edge.
//
// Inputs: a working directory (routed through the S-08.T1 git-root
//
//	anchor), an open *sql.DB already bootstrapped into the `context`
//	storage domain (B/S-02.T2), and caller-supplied user/machine/branch/
//	task/session identifiers this package never invents on its own.
//
// Outputs: SessionScope, ScopeRef, and the CandidateScopeRefs slice, all
//
//	pkg/cascade-typed on every error path.
//
// Constraints: no fifth table, no parallel sessions domain (sessions stay
//
//	in the `sessions` domain per R-16.3); PutEdge rejects any edge that
//	would close a cycle over the parent/member classes at BUILD time
//	(R-21.157); Traversal has no permissive default — an unlisted (kind,
//	edge) pair denies. No bare time.Now; no os.Stdout/Stderr. Every file
//	in this package stays under the 300-line cap.
//
// SPORT: context/scope-model + context/scope-graph +
//
//	context/session-scope-resolver (ADD, per T-4 sport_updates).
package scope
