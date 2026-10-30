// Purpose (this file): the CI executor resolver (P1-CI-03). Resolve walks
// local -> node -> github-actions and returns the first executor that may
// run the sub-job, or ErrNoCapableExecutor. A kind with no registered
// runner is skipped, and journaled as skipped on every resolution; every
// refused step is reported through ResolverDeps.Refusals with its reason.
//
// Inputs: ResolverDeps (Fleet.Lease for the node step, the runner
// registry, the GitHub reader for the hosted guard, the toolchain
// fingerprint, the refusal journal and the clock) and one JobSpec.
// Outputs: a ResolvedExecutor, or an error. Only the node step's four
// capacity refusals (ErrNoCapableTier, ErrDataClassDenied,
// ErrSensitivityConstraint, ErrNoAllowedLanes, matched by identity) fall
// through to the next step; any other Fleet.Lease error fails the sub-job.
// Constraints: fail closed. Local needs a registered local runner, no
// capability beyond clean-room, a matching toolchain and trusted source;
// untrusted source with nothing else available is
// ErrExternalSourceOnController. A node is never ephemeral, so untrusted
// source never goes to one (ADR-0009 section 6). The hosted step needs a
// registered runner, a public-tier job, a github.com origin, a visibility
// read that says public (an error, an unknown answer or a nil reader is
// private) and the commit present on the remote. The resolver never pushes,
// creates a ref or uploads: its only git call reads the origin URL.
// NewResolver imports nothing from the daemon, so a provider package can
// build one.
// SPORT: internal.ci.ResolverDeps/ADDED, internal.ci.Refusal/ADDED,
//
//	internal.ci.Resolver/ADDED, internal.ci.NewResolver/ADDED (P1-CI-03).

package ci

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// The isolation classes of the node and hosted executors (the local one is
// IsolationCleanCheckoutSameUID).
const (
	isolationNodeWorktree = "node-worktree"
	isolationHosted       = "hosted"
	capabilityCleanRoom   = "clean-room"
)

// closedCapabilities is the whole capability set a JobSpec may require.
var closedCapabilities = map[string]bool{capabilityCleanRoom: true, "docker": true, "xcode": true, "gpu": true}

// leaseFleet is the part of capacity.Fleet the resolver and the resolving
// executor use (contract:fleet-lease).
type leaseFleet = interface {
	Lease(ctx context.Context, req capacity.ResourceRequest, d capacity.DispatchRef, attempt int) (capacity.Lease, error)
	Commit(ctx context.Context, l capacity.Lease) error
	Release(ctx context.Context, l capacity.Lease) error
}

// ResolverDeps carries every input NewResolver needs. GitHub may be nil:
// the hosted step is then refused (visibility unknown is private). Fleet
// is required only when a node runner is registered.
type ResolverDeps struct {
	Fleet       leaseFleet
	Runners     RunnerSet
	GitHub      GitHubReader
	Fingerprint func(ctx context.Context) (string, error)
	Refusals    func(ctx context.Context, r Refusal)
	Clock       runtime.Clock
}

// Refusal is one skipped or refused resolution step and why.
type Refusal struct {
	JobID  string
	Kind   RequirementKind
	Step   ExecutorKind
	Reason string
	At     time.Time
}

// Resolver is the production ExecutorResolver. Build it with NewResolver.
type Resolver struct {
	d ResolverDeps
	// git runs a read-only git command in dir; tests replace it to record.
	git func(ctx context.Context, dir string, args ...string) (string, error)
}

// NewResolver validates d and returns a Resolver.
func NewResolver(d ResolverDeps) (*Resolver, error) {
	missing := ""
	switch {
	case len(d.Runners) == 0:
		missing = "Runners"
	case d.Fingerprint == nil:
		missing = "Fingerprint"
	case d.Refusals == nil:
		missing = "Refusals"
	case d.Clock == nil:
		missing = "Clock"
	case d.Runners[ExecutorNode] != nil && d.Fleet == nil:
		missing = "Fleet (a node runner is registered)"
	}
	if missing != "" {
		return nil, cascade.Newf(cascade.KindInvalidInput, "ci: NewResolver requires %s", missing)
	}
	for k, run := range d.Runners {
		if (k != ExecutorLocal && k != ExecutorNode && k != ExecutorGitHubActions) || run == nil {
			return nil, cascade.Newf(cascade.KindInvalidInput, "ci: NewResolver: invalid runner registration %q", k)
		}
	}
	git := func(ctx context.Context, dir string, args ...string) (string, error) {
		return streamGitEnv(ctx, dir, nil, args...)
	}
	return &Resolver{d: d, git: git}, nil
}

// Resolve returns the first executor, in the order local, node,
// github-actions, that may run spec.
func (r *Resolver) Resolve(ctx context.Context, spec JobSpec) (ResolvedExecutor, error) {
	if err := validateSpec(spec); err != nil {
		return ResolvedExecutor{}, err
	}
	for _, k := range []ExecutorKind{ExecutorLocal, ExecutorNode, ExecutorGitHubActions} {
		if r.d.Runners[k] == nil {
			r.refuse(ctx, spec, k, "skipped: no %s runner is registered", k)
		}
	}
	ex, ok, untrustedLocal := r.tryLocal(ctx, spec)
	if ok {
		return ex, nil
	}
	ex, ok, err := r.tryNode(ctx, spec)
	if err != nil || ok {
		return ex, err
	}
	if ex, ok := r.tryHosted(ctx, spec); ok {
		return ex, nil
	}
	if untrustedLocal {
		return ResolvedExecutor{}, cascade.Wrapf(cascade.KindPolicyDenied, ErrExternalSourceOnController,
			"ci: %s %s: source trust %q and no ephemeral executor is available", spec.JobID, spec.Kind, spec.SourceTrust)
	}
	return ResolvedExecutor{}, cascade.Wrapf(cascade.KindUnavailable, ErrNoCapableExecutor,
		"ci: %s %s: no registered executor may run it (required %v)", spec.JobID, spec.Kind, spec.Required)
}

func validateSpec(spec JobSpec) error {
	if !spec.Kind.Valid() || !spec.Sensitivity.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "ci: resolve: invalid kind %q or sensitivity tier", spec.Kind)
	}
	for _, c := range spec.Required {
		if !closedCapabilities[c] {
			return cascade.Newf(cascade.KindInvalidInput, "ci: resolve: unknown capability %q", c)
		}
	}
	return nil
}

// refuse journals one skipped or refused step.
func (r *Resolver) refuse(ctx context.Context, spec JobSpec, step ExecutorKind, format string, args ...any) {
	r.d.Refusals(ctx, Refusal{JobID: spec.JobID, Kind: spec.Kind, Step: step, Reason: fmt.Sprintf(format, args...), At: r.d.Clock.Now()})
}

// tryLocal resolves the controller's clean checkout. untrusted reports a
// refusal that was only for source trust.
func (r *Resolver) tryLocal(ctx context.Context, spec JobSpec) (ex ResolvedExecutor, ok, untrusted bool) {
	if r.d.Runners[ExecutorLocal] == nil {
		return ResolvedExecutor{}, false, false
	}
	if extra := slices.DeleteFunc(slices.Clone(spec.Required), func(c string) bool { return c == capabilityCleanRoom }); len(extra) > 0 {
		r.refuse(ctx, spec, ExecutorLocal, "requires %s, which the controller never provides", strings.Join(extra, ","))
		return ResolvedExecutor{}, false, false
	}
	if spec.ToolchainFP != "" {
		fp, err := r.d.Fingerprint(ctx)
		if err != nil || fp != spec.ToolchainFP {
			r.refuse(ctx, spec, ExecutorLocal, "toolchain mismatch: job %s, controller %q (err=%v)", spec.ToolchainFP, fp, err)
			return ResolvedExecutor{}, false, false
		}
	}
	if spec.SourceTrust != provider.ProvenanceTrusted {
		r.refuse(ctx, spec, ExecutorLocal, "source trust %q is not trusted (ADR-0009 section 6)", spec.SourceTrust)
		return ResolvedExecutor{}, false, true
	}
	env := Environment{RunnerKind: ExecutorLocal, IsolationClass: IsolationCleanCheckoutSameUID, ToolchainFP: spec.ToolchainFP}
	return ResolvedExecutor{Kind: ExecutorLocal, Env: env}, true, false
}

// tryNode leases a node through Fleet.Lease. A non-nil error fails the
// sub-job; the four capacity refusals fall through.
func (r *Resolver) tryNode(ctx context.Context, spec JobSpec) (ResolvedExecutor, bool, error) {
	if r.d.Runners[ExecutorNode] == nil {
		return ResolvedExecutor{}, false, nil
	}
	if spec.SourceTrust != provider.ProvenanceTrusted {
		r.refuse(ctx, spec, ExecutorNode, "source trust %q needs an ephemeral executor; a node worktree is not one", spec.SourceTrust)
		return ResolvedExecutor{}, false, nil
	}
	req, ref := nodeRequest(spec)
	lease, err := r.d.Fleet.Lease(ctx, req, ref, 1)
	if err != nil {
		if leaseFallsThrough(err) {
			r.refuse(ctx, spec, ExecutorNode, "fleet refused the lease: %v", err)
			return ResolvedExecutor{}, false, nil
		}
		return ResolvedExecutor{}, false, cascade.Wrapf(cascade.KindUnavailable, err, "ci: %s %s: node lease failed", spec.JobID, spec.Kind)
	}
	env := Environment{RunnerKind: ExecutorNode, IsolationClass: isolationNodeWorktree, NodeID: lease.NodeID, ToolchainFP: spec.ToolchainFP}
	return ResolvedExecutor{Kind: ExecutorNode, Env: env, Lease: &lease}, true, nil
}

// nodeRequest is the node step's Fleet.Lease input. RiskClass is left to
// ValidateResourceRequest, which fills the strictest class (JobSpec
// carries no risk class).
func nodeRequest(spec JobSpec) (capacity.ResourceRequest, capacity.DispatchRef) {
	caps := slices.Clone(spec.Required)
	if spec.ToolchainFP != "" {
		caps = append(caps, "toolchain:"+spec.ToolchainFP)
	}
	req := capacity.ResourceRequest{Intent: "ci:" + string(spec.Kind), TaskClass: conductor.TaskClassCode,
		MinQuality: capacity.QualityStandard, Deadline: capacity.DeadlineBatch, Sensitivity: spec.Sensitivity}
	ref := capacity.DispatchRef{JobID: spec.JobID, RepoID: spec.RepoID, ProjectID: spec.ProjectID,
		Placement: &capacity.PlacementNeed{Capabilities: caps}, Requests: 1}
	return req, ref
}

// leaseFallsThrough reports the four capacity refusals, by identity.
func leaseFallsThrough(err error) bool {
	return errChainHas(err, capacity.ErrNoCapableTier) || errChainHas(err, capacity.ErrDataClassDenied) ||
		errChainHas(err, capacity.ErrSensitivityConstraint) || errChainHas(err, capacity.ErrNoAllowedLanes)
}

var (
	githubHTTPSOrigin = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9][A-Za-z0-9-]*)/([A-Za-z0-9._-]+?)(?:\.git)?/?$`)
	githubSSHOrigin   = regexp.MustCompile(`^git@github\.com:([A-Za-z0-9][A-Za-z0-9-]*)/([A-Za-z0-9._-]+?)(?:\.git)?$`)
	commitSHA         = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

// parseGitHubOrigin returns owner and repo for a github.com origin URL.
func parseGitHubOrigin(origin string) (owner, repo string, ok bool) {
	origin = strings.TrimSpace(origin)
	for _, re := range []*regexp.Regexp{githubHTTPSOrigin, githubSSHOrigin} {
		if m := re.FindStringSubmatch(origin); m != nil && m[2] != "." && m[2] != ".." {
			return m[1], m[2], true
		}
	}
	return "", "", false
}

// tryHosted resolves a GitHub Actions run, or journals why not.
func (r *Resolver) tryHosted(ctx context.Context, spec JobSpec) (ResolvedExecutor, bool) {
	refuse := func(format string, args ...any) (ResolvedExecutor, bool) {
		r.refuse(ctx, spec, ExecutorGitHubActions, format, args...)
		return ResolvedExecutor{}, false
	}
	switch {
	case r.d.Runners[ExecutorGitHubActions] == nil:
		return ResolvedExecutor{}, false
	case spec.Sensitivity != provider.SensitivityPublic:
		return refuse("job tier %s is not public", spec.Sensitivity)
	case r.d.GitHub == nil:
		return refuse("no GitHub reader: visibility unknown, treated as private")
	}
	origin, err := r.git(ctx, spec.RepoRoot, "remote", "get-url", "origin")
	if err != nil {
		return refuse("reading the origin URL: %v", err)
	}
	owner, repo, ok := parseGitHubOrigin(origin)
	if !ok {
		return refuse("origin %q is not a github.com repository", strings.TrimSpace(origin))
	}
	private, err := r.d.GitHub.RepoVisibility(ctx, owner, repo)
	if err != nil || private {
		return refuse("%s/%s visibility is not public (private=%v, err=%v)", owner, repo, private, err)
	}
	exists, err := r.d.GitHub.CommitExists(ctx, owner, repo, spec.CheckpointCommit)
	if err != nil || !exists {
		return refuse("commit %s is not on %s/%s (err=%v)", spec.CheckpointCommit, owner, repo, err)
	}
	env := Environment{RunnerKind: ExecutorGitHubActions, IsolationClass: isolationHosted, Ephemeral: true, ToolchainFP: spec.ToolchainFP}
	return ResolvedExecutor{Kind: ExecutorGitHubActions, Env: env}, true
}
