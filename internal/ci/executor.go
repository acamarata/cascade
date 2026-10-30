// Purpose (this file): the CI executor resolver's contract types
// (contract:ci-executor-resolver, P1-CI-03): ExecutorKind, Environment,
// JobSpec, ResolvedExecutor, ExecutorResolver, Runner, RunnerSet,
// GitHubReader and the three resolver errors. Types, interfaces and errors
// only: the resolver is executor_resolver.go, the resolving SubJobExecutor
// is executor_subjob.go, the hosted guard and the GitHubReader reads are
// gha_reader.go, and ComputeToolchainFingerprint is executor_unix.go /
// executor_windows.go.
//
// Inputs: none at package scope.
// Outputs: plain data types, interfaces and sentinels.
// Constraints: errors use the frozen 14 cascade Kinds. errors.Is on a
// cascade sentinel compares Kind only, so the resolver and its callers
// match these sentinels by identity (errChainHas), never by Kind.
// SPORT: internal.ci.ExecutorKind/ADDED, internal.ci.Environment/ADDED,
//
//	internal.ci.JobSpec/ADDED, internal.ci.ResolvedExecutor/ADDED,
//	internal.ci.ExecutorResolver/ADDED, internal.ci.Runner/ADDED,
//	internal.ci.RunnerSet/ADDED, internal.ci.GitHubReader/ADDED,
//	internal.ci.ErrNoCapableExecutor/ADDED,
//	internal.ci.ErrPlatformUnsupported/ADDED,
//	internal.ci.ErrExternalSourceOnController/ADDED (P1-CI-03).

package ci

import (
	"context"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ExecutorKind names where a sub-job runs. The set is closed.
type ExecutorKind string

// The three executor kinds, in resolution order.
const (
	// ExecutorLocal is a fresh checkout of the snapshot tree on the controller.
	ExecutorLocal ExecutorKind = "local"
	// ExecutorNode is an enrolled node placed through Fleet.Lease.
	ExecutorNode ExecutorKind = "node"
	// ExecutorGitHubActions is a hosted GitHub Actions run of a public repository.
	ExecutorGitHubActions ExecutorKind = "github-actions"
)

// Environment is the per-run provenance of one sub-job (P2 requirement 13):
// the runner kind, the isolation class ("clean-checkout-same-uid" for local,
// "node-worktree" for a node, "hosted" for GitHub Actions), whether the
// execution environment is ephemeral, the node that ran it and the
// toolchain fingerprint it ran with.
type Environment struct {
	RunnerKind          ExecutorKind
	IsolationClass      string
	Ephemeral           bool
	NodeID, ToolchainFP string
}

// JobSpec is what the resolver needs to place one sub-job. Required is a
// subset of the closed capability set clean-room, docker, xcode and gpu;
// ToolchainFP, when set, is the toolchain fingerprint the job must run
// with; SourceTrust is the provenance of the code under test (anything
// other than provider.ProvenanceTrusted is external).
type JobSpec struct {
	Kind                                                 RequirementKind
	Required                                             []string
	ToolchainFP                                          string
	Sensitivity                                          provider.SensitivityTier
	SourceTrust                                          provider.Provenance
	CheckpointCommit, RepoRoot, JobID, ProjectID, RepoID string
}

// ResolvedExecutor is the resolver's choice for one sub-job. Lease is set
// for the node kind only: the resolving executor commits it when the run
// starts and releases it when the run ends or fails to start.
type ResolvedExecutor struct {
	Kind  ExecutorKind
	Env   Environment
	Lease *capacity.Lease
}

// ExecutorResolver chooses the executor for one sub-job.
type ExecutorResolver interface {
	Resolve(ctx context.Context, spec JobSpec) (ResolvedExecutor, error)
}

// Runner runs one sub-job on the executor the resolver chose.
type Runner interface {
	Run(ctx context.Context, ex ResolvedExecutor, sj SubJob) (SubJobResult, error)
}

// RunnerSet is the runner registry: a kind with no registered runner is
// never resolved.
type RunnerSet map[ExecutorKind]Runner

// GitHubReader is the two read-only GitHub queries the hosted guard needs.
// A visibility it cannot establish is private.
type GitHubReader interface {
	RepoVisibility(ctx context.Context, owner, repo string) (private bool, err error)
	CommitExists(ctx context.Context, owner, repo, sha string) (bool, error)
}

// The resolver errors.
var (
	// ErrNoCapableExecutor reports that no registered executor may run the sub-job.
	ErrNoCapableExecutor = cascade.New(cascade.KindUnavailable, "ci: executor: no capable executor for this sub-job")
	// ErrPlatformUnsupported reports that the toolchain fingerprint cannot be computed on this platform (Windows is tier 2).
	ErrPlatformUnsupported = cascade.New(cascade.KindUnsupported, "ci: executor: toolchain fingerprint is not supported on this platform")
	// ErrExternalSourceOnController reports external-source code that only the controller could run (ADR-0009 section 6).
	ErrExternalSourceOnController = cascade.New(cascade.KindPolicyDenied, "ci: executor: external-source code is never verified on the controller")
)
