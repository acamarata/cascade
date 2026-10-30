package ci

// Tests for the resolver's node step and hosted guard (P1-CI-03).

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestExecutorPairedNodeRequestValid: the node step's request passes
// capacity.ValidateResourceRequest with the ci:<kind> intent and the batch
// deadline, and leases once (attempt 1).
func TestExecutorPairedNodeRequestValid(t *testing.T) {
	fleet := &fakeFleet{}
	rig := newResolverRig(t, RunnerSet{ExecutorNode: &recRunner{}}, fleet, nil)
	if _, err := rig.r.Resolve(context.Background(), testSpec(RequirementCompile)); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(fleet.reqs) != 1 {
		t.Fatalf("leases = %d, want 1", len(fleet.reqs))
	}
	req := fleet.reqs[0]
	if _, err := capacity.ValidateResourceRequest(req, nil); err != nil {
		t.Fatalf("ValidateResourceRequest(%+v): %v", req, err)
	}
	if req.Intent != "ci:compile" || req.Deadline != capacity.DeadlineBatch || req.MinQuality != capacity.QualityStandard {
		t.Fatalf("request = %+v; want intent ci:compile, batch, standard", req)
	}
}

// TestExecutorPairedNodeCarriesSensitivity: the request carries the job's
// tier and the DispatchRef carries ProjectID, Requests 1 and the required
// capabilities plus toolchain:<fp>.
func TestExecutorPairedNodeCarriesSensitivity(t *testing.T) {
	for _, tier := range []provider.SensitivityTier{provider.SensitivityRestricted, provider.SensitivityLocalOnly, provider.SensitivityPublic} {
		fleet := &fakeFleet{}
		rig := newResolverRig(t, RunnerSet{ExecutorNode: &recRunner{}}, fleet, nil)
		spec := testSpec(RequirementIntegration)
		spec.Sensitivity, spec.ToolchainFP = tier, testFP
		if _, err := rig.r.Resolve(context.Background(), spec); err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		req, ref := fleet.reqs[0], fleet.refs[0]
		caps := ref.Placement.Capabilities
		if req.Sensitivity != tier || ref.ProjectID != "proj-1" || ref.JobID != "job-1" || ref.RepoID != "repo-1" || ref.Requests != 1 ||
			!slices.Equal(caps, []string{"clean-room", "toolchain:" + testFP}) {
			t.Fatalf("tier %s: request %+v ref %+v; want the job tier, ProjectID, Requests 1 and caps", tier, req, ref)
		}
	}
}

// TestExecutorPairedNodeOtherErrorFailsSubJob: a Fleet.Lease error that is
// not one of the four refusals fails resolution, even one sharing a
// refusal's Kind, and never reaches the hosted step.
func TestExecutorPairedNodeOtherErrorFailsSubJob(t *testing.T) {
	for _, leaseErr := range []error{
		cascade.New(cascade.KindUnavailable, "capacity: database is locked"),
		cascade.New(cascade.KindPolicyDenied, "capacity: some other policy"),
		capacity.ErrFleetExhausted,
	} {
		gh := &fakeReader{exists: true}
		rig := newResolverRig(t, RunnerSet{ExecutorNode: &recRunner{}, ExecutorGitHubActions: &recRunner{}}, &fakeFleet{leaseErr: leaseErr}, gh)
		spec := testSpec(RequirementLint)
		spec.Sensitivity = provider.SensitivityPublic
		ex, err := rig.r.Resolve(context.Background(), spec)
		if err == nil || errChainHas(err, ErrNoCapableExecutor) || !errChainHas(err, leaseErr) || gh.visCalls != 0 {
			t.Fatalf("lease error %v: Resolve = %+v, %v, hosted reads %d; want the sub-job failed", leaseErr, ex, err, gh.visCalls)
		}
	}
}

// TestExecutorNoAllowedLanesFallsThrough: each of the four capacity
// refusals, bare or wrapped, falls through to the hosted step.
func TestExecutorNoAllowedLanesFallsThrough(t *testing.T) {
	for _, sentinel := range []error{capacity.ErrNoAllowedLanes, capacity.ErrNoCapableTier, capacity.ErrDataClassDenied, capacity.ErrSensitivityConstraint} {
		for _, leaseErr := range []error{sentinel, fmt.Errorf("lease: %w", sentinel)} {
			rig := newResolverRig(t, RunnerSet{ExecutorNode: &recRunner{}, ExecutorGitHubActions: &recRunner{}}, &fakeFleet{leaseErr: leaseErr}, &fakeReader{exists: true})
			spec := testSpec(RequirementLint)
			spec.Sensitivity = provider.SensitivityPublic
			ex, err := rig.r.Resolve(context.Background(), spec)
			nodeRefused := slices.ContainsFunc(rig.refusals, func(r Refusal) bool {
				return r.Step == ExecutorNode && strings.HasPrefix(r.Reason, "fleet refused the lease")
			})
			if err != nil || ex.Kind != ExecutorGitHubActions || !nodeRefused {
				t.Fatalf("lease error %v: Resolve = %+v, %v, refusals %+v; want fall-through to hosted", leaseErr, ex, err, rig.refusals)
			}
		}
	}
}

// TestExecutorGHARefusesNonPublicTier: a non-public job never resolves
// hosted and never reads GitHub.
func TestExecutorGHARefusesNonPublicTier(t *testing.T) {
	for _, tier := range []provider.SensitivityTier{provider.SensitivityRestricted, provider.SensitivityLocalOnly, provider.SensitivityInternal} {
		gh := &fakeReader{exists: true}
		rig := newResolverRig(t, RunnerSet{ExecutorGitHubActions: &recRunner{}}, nil, gh)
		spec := testSpec(RequirementLint)
		spec.Sensitivity = tier
		if _, err := rig.r.Resolve(context.Background(), spec); !errChainHas(err, ErrNoCapableExecutor) || gh.visCalls+gh.shaCalls != 0 || len(rig.gitCalls) != 0 {
			t.Fatalf("tier %s: Resolve = %v, reads %d, git %v; want refusal with no read", tier, err, gh.visCalls+gh.shaCalls, rig.gitCalls)
		}
	}
}

// TestExecutorGHARefusesCommitNotOnRemote: a commit the remote does not
// have, or one the lookup could not confirm, is refused.
func TestExecutorGHARefusesCommitNotOnRemote(t *testing.T) {
	for _, gh := range []*fakeReader{{exists: false}, {exists: true, existsErr: errors.New("HTTP 502")}} {
		rig := newResolverRig(t, RunnerSet{ExecutorGitHubActions: &recRunner{}}, nil, gh)
		spec := testSpec(RequirementLint)
		spec.Sensitivity = provider.SensitivityPublic
		if _, err := rig.r.Resolve(context.Background(), spec); !errChainHas(err, ErrNoCapableExecutor) || gh.shaCalls != 1 {
			t.Fatalf("reader %+v: Resolve = %v; want refusal after one commit lookup", gh, err)
		}
	}
}

// TestExecutorGHANeverPushes resolves and runs a hosted sub-job over a
// real repository through the real git reader, recording every git call:
// the only call reads the origin URL, and no ref changes.
func TestExecutorGHANeverPushes(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=ci@example.invalid", "-c", "user.name=ci"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "c")
	git("remote", "add", "origin", "git@github.com:acme/widget.git")
	refs := git("for-each-ref") + git("config", "--get-regexp", "remote")
	hosted := &recRunner{res: SubJobResult{RunID: -1, RepoID: 1, Passed: true}}
	runners := RunnerSet{ExecutorGitHubActions: hosted}
	rig := newResolverRig(t, runners, nil, &fakeReader{exists: true})
	var calls [][]string
	rig.r.git = func(ctx context.Context, d string, args ...string) (string, error) {
		calls = append(calls, args)
		return streamGitEnv(ctx, d, nil, args...)
	}
	sj := testSubJob(t, RequirementLint)
	sj.Ref.RepoRoot, sj.Ref.Sensitivity = filepath.Clean(dir), provider.SensitivityPublic
	res, err := newTestResolving(t, rig.r, runners, nil, trusted).Run(context.Background(), sj)
	if err != nil || res.ExecutorKind != string(ExecutorGitHubActions) || hosted.n() != 1 {
		t.Fatalf("Run = %+v, %v; want one hosted run", res, err)
	}
	if len(calls) != 1 || strings.Join(calls[0], " ") != "remote get-url origin" {
		t.Fatalf("git calls = %v; want exactly one origin read", calls)
	}
	if after := git("for-each-ref") + git("config", "--get-regexp", "remote"); after != refs {
		t.Fatalf("refs or remotes changed:\nbefore %s\nafter %s", refs, after)
	}
}
