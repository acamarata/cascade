// Purpose: P1-E25-W5-S51-T4 -- the PUSH side of CI failure-to-attention
// routing: the data-class/scope derivation an attention item is filed
// under, and Router, the production AttentionPusher that performs every
// push through supervision.RoutePush (routing.go) rather than calling
// Store.Push directly, so R-21.157(b)'s data-class check and global-shape
// check both run on this producer's items. Split from attention.go purely
// to keep that file inside Art.10.3's 300-line cap.
//
// SCOPE/DATA CLASS. A repository listed in [ci.policy.repos].private
// (config_ci.go, the never-pay policy's own private list) is private
// material: its name, ref and failing-job names are filed at PROJECT
// scope keyed by the repo, never in a GLOBAL-scope item every scope in
// the tree can see. Public repos keep the global scope `cascade fleet
// attention list` reads by default. NewCIDataClassChecker is the
// fail-closed backstop for that rule: it refuses a GLOBAL-scope item
// whose SourceRef names a private repo, so a future regression in the
// scope derivation is a typed KindPermissionDenied refusal surfaced to
// the operator, not a silent leak.
//
// ORIGIN == TARGET. Router always passes PushRequest.Origin equal to the
// item's own ScopeRef: this producer files into the scope the material
// belongs to and never ADDRESSES a push at a foreign scope, so
// routing.go's cross-scope capability rule (inbox.send /
// inbox.cross_scope_send) is correctly not engaged, while checkDataClass
// and checkGlobalShape still are.
//
// Inputs: a *supervision.Store (production: daemon.NewAttentionStore over
// the runtime store) plus the [ci.policy.repos] private patterns.
// Outputs: a pushed supervision.AttentionItem, or a typed refusal.
// Constraints: no new architecture -- RoutePush and Store are used as
// shipped; this file adds only the adapter that satisfies AttentionPusher
// and AttentionLister.
// SPORT: internal.ci.Router/ADDED, internal.ci.NewCIDataClassChecker/ADDED,
//
//	internal.ci.PlatformRefusal/ADDED (P1-E25-W5-S51-T4).

package ci

import (
	"context"
	"path"
	"strings"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/pkg/cascade"
)

// isCIConclusionError reports whether err is WaitOnGreen's "the run
// concluded something other than success" signal (KindConflict) as
// opposed to a wait-loop timeout, a cancellation, or a platform refusal.
func isCIConclusionError(err error) bool {
	kind, ok := cascade.KindOf(err)
	return ok && kind == cascade.KindConflict
}

// privateRepoMatch reports whether repo matches any [ci.policy.repos]
// private glob (path.Match semantics -- the same matcher config_ci.go's
// validateRepoPattern validates against).
func privateRepoMatch(private []string, repo string) bool {
	for _, pattern := range private {
		trimmed := strings.TrimSpace(pattern)
		if trimmed == "" {
			continue
		}
		if ok, err := path.Match(trimmed, repo); err == nil && ok {
			return true
		}
	}
	return false
}

// candidateScope derives the scope an item for candidate is filed under.
func candidateScope(candidate AttentionCandidate, private []string) supervision.ScopeRef {
	if privateRepoMatch(private, candidate.Repo) {
		return supervision.ScopeRef{Kind: scope.ScopeKindProject, ID: candidate.Repo}
	}
	return supervision.ScopeRef{Kind: scope.ScopeKindGlobal}
}

// repoFromSourceRef recovers the repo named by an `ci:<repo>:<run_id>`
// SourceRef. A repo contains a slash but never a colon, so the LAST colon
// separates the run id.
func repoFromSourceRef(sourceRef string) (string, bool) {
	if !strings.HasPrefix(sourceRef, attentionSourceRefPrefix) {
		return "", false
	}
	rest := strings.TrimPrefix(sourceRef, attentionSourceRefPrefix)
	idx := strings.LastIndex(rest, ":")
	if idx <= 0 {
		return "", false
	}
	return rest[:idx], true
}

// ciDataClass is the DataClassChecker described in this file's header.
type ciDataClass struct {
	private []string
}

// Allow refuses a GLOBAL-scope item whose SourceRef names a private
// repository. Every other item passes: this checker governs THIS
// producer's private-repo rule and nothing else.
func (c ciDataClass) Allow(_ context.Context, item supervision.AttentionItem) (bool, error) {
	if item.ScopeRef.Kind != scope.ScopeKindGlobal {
		return true, nil
	}
	repo, ok := repoFromSourceRef(item.SourceRef)
	if !ok {
		return true, nil
	}
	return !privateRepoMatch(c.private, repo), nil
}

// NewCIDataClassChecker returns the data-class checker Router applies to
// every push. A nil/empty private list yields a checker that allows
// everything, which is the correct answer when no repository has been
// declared private.
func NewCIDataClassChecker(private []string) supervision.DataClassChecker {
	return ciDataClass{private: private}
}

// Router is the production AttentionPusher: a thin adapter over a real
// *supervision.Store that performs every push through
// supervision.RoutePush. It satisfies AttentionLister too, by delegating
// to the same Store, so RouteResult can report Existing/Acked honestly.
type Router struct {
	Store *supervision.Store
	// Class is the data-class policy applied before delivery. A nil
	// Class means "no data-class policy configured" and always passes,
	// exactly as routing.go's DataClassChecker doc comment specifies.
	Class supervision.DataClassChecker
	// Subject identifies who is pushing. It is only consulted for a
	// CROSS-scope push, which this producer never performs (see the
	// header's ORIGIN == TARGET note), so the zero value is correct.
	Subject supervision.GrantCheckRequest
}

// Push routes item through supervision.RoutePush.
func (r Router) Push(ctx context.Context, item supervision.AttentionItem) (supervision.AttentionItem, error) {
	if r.Store == nil {
		return supervision.AttentionItem{}, cascade.New(cascade.KindInternal,
			"ci: attention Router used with no supervision store")
	}
	return supervision.RoutePush(ctx, r.Store, nil, r.Class, supervision.PushRequest{
		Item: item, Origin: item.ScopeRef, Subject: r.Subject,
	})
}

// ListInScopes delegates the existence probe to the same Store.
func (r Router) ListInScopes(
	ctx context.Context, scopes []supervision.ScopeRef, f supervision.Filter,
) ([]supervision.AttentionItem, error) {
	if r.Store == nil {
		return nil, cascade.New(cascade.KindInternal, "ci: attention Router used with no supervision store")
	}
	return r.Store.ListInScopes(ctx, scopes, f)
}

// PlatformRefusal reports this platform's daemon-absence refusal, or nil
// where a daemon exists. It is the SAME build-tag-split verdict
// WaitOnGreen's own gate consults (waitmerge_unix.go /
// waitmerge_windows.go), exported so `cascade github ci watch add` can
// answer AC6's Windows tier-2 refusal WITHOUT opening -- and thereby
// CREATING -- the runtime store just to learn the platform's answer.
func PlatformRefusal() error { return waitPlatformRefusal() }
