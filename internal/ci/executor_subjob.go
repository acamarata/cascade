// Purpose (this file): the resolving SubJobExecutor (P1-CI-03). Run builds
// a JobSpec from the sub-job, asks the ExecutorResolver where it runs and
// hands it to that kind's Runner. A node lease is committed when the run
// starts and released when it ends or fails to start. Every result carries
// the resolved Environment (per-run provenance, P2 requirement 13).
//
// A sub-job no executor may run (ErrNoCapableExecutor,
// ErrExternalSourceOnController or ErrPlatformUnsupported from the
// resolver, or ErrNoCapableExecutor from the chosen runner, such as the
// local executor with no [ci.local] command for the kind, or a
// toolchain-bound kind whose fingerprint cannot be computed) ends as an
// explicit non-pass terminal (T0 P1-BF-R294, which this retires from the
// daemon): one ci_run completed/skipped named like a stream run, so a
// redispatch reuses it, one ci_job naming the reason, Passed=false and
// ExecutorKind "unresolved". It is never a pass and never a proxy onto
// another command.
//
// Inputs: the resolver, the runner registry, the fleet that granted node
// leases, and ResolvingDeps (the ci_results handle, the clock, the
// toolchain fingerprint and the source-trust reader).
// Outputs: a SubJobExecutor.
// Constraints: fail closed. A nil SourceTrust reader means untrusted. The
// toolchain list ([ci].toolchain_versions) is installed with
// SetToolchainVersions and read on every fingerprint.
// SPORT: internal.ci.NewResolvingSubJobExecutor/ADDED,
//
//	internal.ci.ResolvingDeps/ADDED, internal.ci.SetToolchainVersions/ADDED (P1-CI-03).

package ci

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// executorKindUnresolved is the ExecutorKind of an explicit non-pass
// terminal no executor ran.
const executorKindUnresolved = "unresolved"

// ResolvingDeps carries what the resolving executor needs beyond the
// resolver, the runners and the fleet.
type ResolvingDeps struct {
	CIDB        *sql.DB
	Clock       runtime.Clock
	Fingerprint func(ctx context.Context) (string, error)
	// SourceTrust reads the provenance of the code a sub-job verifies; nil
	// means untrusted.
	SourceTrust func(ctx context.Context, sj SubJob) (provider.Provenance, error)
}

type resolvingExecutor struct {
	r       ExecutorResolver
	runners RunnerSet
	fleet   leaseFleet
	d       ResolvingDeps
}

// NewResolvingSubJobExecutor returns the SubJobExecutor that resolves every
// sub-job through r and runs it on runners. fleet commits and releases the
// node leases r grants; it may be nil when no node runner is registered.
func NewResolvingSubJobExecutor(r ExecutorResolver, runners RunnerSet, fleet leaseFleet, d ResolvingDeps) SubJobExecutor {
	return &resolvingExecutor{r: r, runners: runners, fleet: fleet, d: d}
}

func (e *resolvingExecutor) Run(ctx context.Context, sj SubJob) (SubJobResult, error) {
	if e.r == nil || e.d.CIDB == nil || e.d.Clock == nil || e.d.Fingerprint == nil {
		return SubJobResult{}, cascade.New(cascade.KindInvalidInput, "ci: resolving executor is missing its resolver, CIDB, Clock or Fingerprint")
	}
	spec, err := e.spec(ctx, sj)
	if unresolvable(err) {
		return e.unresolved(ctx, sj, err)
	}
	if err != nil {
		return SubJobResult{}, err
	}
	ex, err := e.r.Resolve(ctx, spec)
	if unresolvable(err) {
		return e.unresolved(ctx, sj, err)
	}
	if err != nil {
		return SubJobResult{}, err
	}
	res, err := e.runResolved(ctx, ex, sj)
	if unresolvable(err) {
		return e.unresolved(ctx, sj, err)
	}
	return res, err
}

func unresolvable(err error) bool {
	return errChainHas(err, ErrNoCapableExecutor) || errChainHas(err, ErrExternalSourceOnController) ||
		errChainHas(err, ErrPlatformUnsupported)
}

// spec builds the sub-job's JobSpec.
func (e *resolvingExecutor) spec(ctx context.Context, sj SubJob) (JobSpec, error) {
	required, toolchain, err := requiredFor(sj.Kind)
	if err != nil {
		return JobSpec{}, err
	}
	spec := JobSpec{Kind: sj.Kind, Required: required, Sensitivity: sj.Ref.Sensitivity,
		CheckpointCommit: sj.Ref.CheckpointCommit, RepoRoot: sj.Ref.RepoRoot, JobID: sj.Ref.JobID,
		ProjectID: sj.Ref.ProjectID, RepoID: sj.Ref.Lease.RepoID}
	if toolchain {
		if spec.ToolchainFP, err = e.d.Fingerprint(ctx); err != nil {
			// No fingerprint, no toolchain-bound executor: an explicit
			// non-pass for this attempt, never a retry on every start.
			return JobSpec{}, cascade.Wrapf(cascade.KindUnavailable, errors.Join(ErrNoCapableExecutor, err),
				"ci: %s needs the toolchain fingerprint, which is unavailable", sj.Kind)
		}
	}
	if e.d.SourceTrust != nil {
		if spec.SourceTrust, err = e.d.SourceTrust(ctx, sj); err != nil {
			return JobSpec{}, err
		}
	}
	return spec, nil
}

// requiredFor maps a requirement kind onto its capabilities and whether it
// needs the toolchain: format and lint need nothing, compile and unit the
// toolchain, integration, security and architecture a clean room (docker is
// never required).
func requiredFor(kind RequirementKind) ([]string, bool, error) {
	switch kind {
	case RequirementFormat, RequirementLint:
		return []string{}, false, nil
	case RequirementCompile, RequirementUnit:
		return []string{}, true, nil
	case RequirementIntegration, RequirementSecurity, RequirementArchitecture:
		return []string{capabilityCleanRoom}, false, nil
	}
	return nil, false, cascade.Newf(cascade.KindInvalidInput, "ci: unknown requirement kind %q", kind)
}

// runResolved runs sj on ex's runner, holding ex's node lease for the run.
func (e *resolvingExecutor) runResolved(ctx context.Context, ex ResolvedExecutor, sj SubJob) (SubJobResult, error) {
	runner := e.runners[ex.Kind]
	if ex.Lease != nil {
		if runner == nil || e.fleet == nil {
			return SubJobResult{}, errors.Join(cascade.Newf(cascade.KindInternal, "ci: %s lease granted with no runner or fleet", ex.Kind), e.release(ctx, ex))
		}
		if err := e.fleet.Commit(ctx, *ex.Lease); err != nil {
			return SubJobResult{}, errors.Join(err, e.release(ctx, ex))
		}
	}
	if runner == nil {
		return SubJobResult{}, cascade.Newf(cascade.KindInternal, "ci: resolved kind %q has no registered runner", ex.Kind)
	}
	res, err := runner.Run(ctx, ex, sj)
	if ex.Lease != nil {
		err = errors.Join(err, e.release(ctx, ex))
	}
	if err != nil {
		return SubJobResult{}, err
	}
	res.ExecutorKind, res.NodeID, res.Env = string(ex.Kind), ex.Env.NodeID, ex.Env
	return res, nil
}

func (e *resolvingExecutor) release(ctx context.Context, ex ResolvedExecutor) error {
	if ex.Lease == nil || e.fleet == nil {
		return nil
	}
	return e.fleet.Release(context.WithoutCancel(ctx), *ex.Lease)
}

// unresolved records sj's explicit non-pass terminal.
func (e *resolvingExecutor) unresolved(ctx context.Context, sj SubJob, cause error) (SubJobResult, error) {
	if !sj.Kind.Valid() || sj.Ref.RepoRoot == "" {
		return SubJobResult{}, errors.Join(cascade.New(cascade.KindInvalidInput, "ci: unresolved sub-job has no kind or repository"), cause)
	}
	repoID, name := LocalRepoID(sj.Ref.RepoRoot), streamRunName(sj)
	var runID int64
	err := e.d.CIDB.QueryRowContext(ctx, `SELECT run_id FROM `+tableRun+` WHERE repo_id = ? AND name = ? AND run_id < 0
		ORDER BY run_id LIMIT 1`, repoID, name).Scan(&runID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SubJobResult{}, cascade.Wrap(cascade.KindUnavailable, err, "ci: looking up an unresolved run")
	}
	now := e.d.Clock.Now()
	if runID == 0 {
		if runID, err = ReserveLocalRun(ctx, e.d.CIDB, repoID, name, now); err != nil {
			return SubJobResult{}, err
		}
	}
	reason := cause.Error()
	if len(reason) > 300 {
		reason = strings.ToValidUTF8(reason[:300], "")
	}
	run := Run{RunID: runID, RepoID: repoID, Name: name, HeadSHA: sj.Ref.CheckpointCommit,
		Status: RunStatusCompleted, Conclusion: ConclusionSkipped, CreatedAt: now, UpdatedAt: now}
	job := Job{JobID: runID, RunID: runID, Name: string(sj.Kind) + ": not run: " + reason,
		Status: RunStatusCompleted, Conclusion: ConclusionSkipped, StartedAt: now, FinishedAt: now}
	if err := Upsert(ctx, e.d.CIDB, run, []Job{job}, nil); err != nil {
		return SubJobResult{}, err
	}
	return SubJobResult{RunID: runID, RepoID: repoID, Passed: false, ExecutorKind: executorKindUnresolved}, nil
}

// installedToolchainVersions is [ci].toolchain_versions as last installed.
var installedToolchainVersions struct {
	mu sync.RWMutex
	v  []string
}

// SetToolchainVersions installs [ci].toolchain_versions; every later
// fingerprint hashes the new list.
func SetToolchainVersions(v []string) {
	installedToolchainVersions.mu.Lock()
	defer installedToolchainVersions.mu.Unlock()
	installedToolchainVersions.v = slices.Clone(v)
}

// fingerprintOf is lowercase hex sha256 of goVersion | unameSM | the sorted
// toolchain list.
func fingerprintOf(goVersion, unameSM string) string {
	installedToolchainVersions.mu.RLock()
	versions := slices.Clone(installedToolchainVersions.v)
	installedToolchainVersions.mu.RUnlock()
	slices.Sort(versions)
	parts := append([]string{strings.TrimSpace(goVersion), strings.TrimSpace(unameSM)}, versions...)
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}
