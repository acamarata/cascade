package ci

// Tests for the executor resolver (P1-CI-03): the shared rig, the two
// red-first fail-closed tests and the resolution order.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

const testFP = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type recRunner struct {
	mu    sync.Mutex
	calls []ResolvedExecutor
	res   SubJobResult
	err   error
}

func (r *recRunner) Run(_ context.Context, ex ResolvedExecutor, _ SubJob) (SubJobResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, ex)
	return r.res, r.err
}

func (r *recRunner) n() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.calls) }

type fakeFleet struct {
	mu        sync.Mutex
	leaseErr  error
	commitErr error
	reqs      []capacity.ResourceRequest
	refs      []capacity.DispatchRef
	events    []string
}

func (f *fakeFleet) Lease(_ context.Context, req capacity.ResourceRequest, d capacity.DispatchRef, _ int) (capacity.Lease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs, f.refs = append(f.reqs, req), append(f.refs, d)
	f.events = append(f.events, "lease")
	if f.leaseErr != nil {
		return capacity.Lease{}, f.leaseErr
	}
	return capacity.Lease{ID: "lease-1", NodeID: "node-7", Sensitivity: req.Sensitivity, RepoID: d.RepoID}, nil
}

func (f *fakeFleet) Commit(_ context.Context, l capacity.Lease) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "commit:"+l.ID)
	return f.commitErr
}

func (f *fakeFleet) Release(_ context.Context, l capacity.Lease) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "release:"+l.ID)
	return nil
}

type fakeReader struct {
	private   bool
	visErr    error
	exists    bool
	existsErr error
	visCalls  int
	shaCalls  int
}

func (g *fakeReader) RepoVisibility(context.Context, string, string) (bool, error) {
	g.visCalls++
	return g.private, g.visErr
}

func (g *fakeReader) CommitExists(context.Context, string, string, string) (bool, error) {
	g.shaCalls++
	return g.exists, g.existsErr
}

type resolverRig struct {
	r        *Resolver
	refusals []Refusal
	gitCalls [][]string
	origin   string
}

// newResolverRig builds a Resolver over runners whose fingerprint is
// testFP and whose git reads a github.com origin and records every call.
func newResolverRig(t *testing.T, runners RunnerSet, fleet leaseFleet, gh GitHubReader) *resolverRig {
	t.Helper()
	rig := &resolverRig{origin: "https://github.com/acme/widget.git\n"}
	deps := ResolverDeps{Runners: runners, GitHub: gh, Clock: runtime.NewFixedClock(time.Unix(1_700_000_000, 0)),
		Fingerprint: func(context.Context) (string, error) { return testFP, nil },
		Refusals:    func(_ context.Context, r Refusal) { rig.refusals = append(rig.refusals, r) }}
	if fleet != nil {
		deps.Fleet = fleet
	}
	r, err := NewResolver(deps)
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	r.git = func(_ context.Context, _ string, args ...string) (string, error) {
		rig.gitCalls = append(rig.gitCalls, args)
		return rig.origin, nil
	}
	rig.r = r
	return rig
}

func testSpec(kind RequirementKind) JobSpec {
	required, toolchain, _ := requiredFor(kind)
	s := JobSpec{Kind: kind, Required: required, Sensitivity: provider.SensitivityInternal,
		SourceTrust: provider.ProvenanceTrusted, CheckpointCommit: strings.Repeat("a", 40), RepoRoot: "/repo",
		JobID: "job-1", ProjectID: "proj-1", RepoID: "repo-1"}
	if toolchain {
		s.ToolchainFP = testFP
	}
	return s
}

// TestExecutorGHAUnknownVisibilityRefused: a visibility read that errors,
// says private, or has no reader never resolves the hosted executor; a
// public answer with the commit present does (the positive control).
func TestExecutorGHAUnknownVisibilityRefused(t *testing.T) {
	spec := testSpec(RequirementLint)
	spec.Sensitivity = provider.SensitivityPublic
	cases := []struct {
		name string
		gh   *fakeReader
	}{
		{"error", &fakeReader{private: false, visErr: errors.New("rate limited"), exists: true}},
		{"private", &fakeReader{private: true, exists: true}},
		{"no reader", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gh GitHubReader
			if c.gh != nil {
				gh = c.gh
			}
			rig := newResolverRig(t, RunnerSet{ExecutorGitHubActions: &recRunner{}}, nil, gh)
			_, err := rig.r.Resolve(context.Background(), spec)
			if !errChainHas(err, ErrNoCapableExecutor) {
				t.Fatalf("Resolve = %v, want ErrNoCapableExecutor", err)
			}
			if c.gh != nil && c.gh.shaCalls != 0 {
				t.Fatalf("CommitExists called %d times after a non-public visibility", c.gh.shaCalls)
			}
		})
	}
	for _, origin := range []string{"https://gitlab.com/acme/widget.git", "git@github.com.evil.example:acme/widget.git", ""} {
		gh := &fakeReader{exists: true}
		rig := newResolverRig(t, RunnerSet{ExecutorGitHubActions: &recRunner{}}, nil, gh)
		rig.origin = origin
		if _, err := rig.r.Resolve(context.Background(), spec); !errChainHas(err, ErrNoCapableExecutor) || gh.visCalls != 0 {
			t.Fatalf("origin %q: Resolve = %v, reads %d; want refusal before any visibility read", origin, err, gh.visCalls)
		}
	}
	rig := newResolverRig(t, RunnerSet{ExecutorGitHubActions: &recRunner{}}, nil, &fakeReader{exists: true})
	ex, err := rig.r.Resolve(context.Background(), spec)
	if err != nil || ex.Kind != ExecutorGitHubActions || !ex.Env.Ephemeral || ex.Env.IsolationClass != isolationHosted {
		t.Fatalf("public control: Resolve = %+v, %v; want the hosted ephemeral executor", ex, err)
	}
}

// TestExecutorExternalSourceNeverLocal: untrusted or unknown source with
// only local registered is ErrExternalSourceOnController and runs nothing;
// with a node registered too, the node is refused (not ephemeral) and no
// lease is asked for.
func TestExecutorExternalSourceNeverLocal(t *testing.T) {
	for _, trust := range []provider.Provenance{provider.ProvenanceUntrustedSource, "", "owner"} {
		local := &recRunner{}
		spec := testSpec(RequirementUnit)
		spec.SourceTrust = trust
		rig := newResolverRig(t, RunnerSet{ExecutorLocal: local}, nil, nil)
		if _, err := rig.r.Resolve(context.Background(), spec); !errChainHas(err, ErrExternalSourceOnController) {
			t.Fatalf("trust %q: Resolve = %v, want ErrExternalSourceOnController", trust, err)
		}
		fleet := &fakeFleet{}
		rig = newResolverRig(t, RunnerSet{ExecutorLocal: local, ExecutorNode: &recRunner{}}, fleet, nil)
		if _, err := rig.r.Resolve(context.Background(), spec); !errChainHas(err, ErrExternalSourceOnController) || len(fleet.reqs) != 0 {
			t.Fatalf("trust %q with a node: Resolve = %v, leases %d; want refusal and no lease", trust, err, len(fleet.reqs))
		}
		res, err := newTestResolving(t, rig.r, RunnerSet{ExecutorLocal: local}, nil, nil).Run(context.Background(), testSubJob(t, RequirementUnit))
		if err != nil || res.Passed || res.ExecutorKind != executorKindUnresolved || local.n() != 0 {
			t.Fatalf("resolving run = %+v, %v, local calls %d; want an unresolved non-pass and nothing run", res, err, local.n())
		}
	}
	rig := newResolverRig(t, RunnerSet{ExecutorLocal: &recRunner{}}, nil, nil)
	if ex, err := rig.r.Resolve(context.Background(), testSpec(RequirementUnit)); err != nil || ex.Kind != ExecutorLocal {
		t.Fatalf("trusted control: Resolve = %+v, %v; want local", ex, err)
	}
}

// TestExecutorResolutionOrder walks local -> node -> hosted -> exhaustion
// with test runners registered here.
func TestExecutorResolutionOrder(t *testing.T) {
	all := func() RunnerSet {
		return RunnerSet{ExecutorLocal: &recRunner{}, ExecutorNode: &recRunner{}, ExecutorGitHubActions: &recRunner{}}
	}
	without := func(k ExecutorKind) RunnerSet { s := all(); delete(s, k); return s }
	spec := func(kind RequirementKind, edit func(*JobSpec)) JobSpec { s := testSpec(kind); edit(&s); return s }
	noop := func(*JobSpec) {}
	cases := []struct {
		name     string
		runners  RunnerSet
		spec     JobSpec
		leaseErr error
		want     ExecutorKind
		wantErr  error
	}{
		{"local", all(), spec(RequirementLint, noop), nil, ExecutorLocal, nil},
		{"node", without(ExecutorLocal), spec(RequirementCompile, noop), nil, ExecutorNode, nil},
		{"hosted", RunnerSet{ExecutorGitHubActions: &recRunner{}}, spec(RequirementLint, func(s *JobSpec) { s.Sensitivity = provider.SensitivityPublic }), nil, ExecutorGitHubActions, nil},
		{"exhaustion", all(), spec(RequirementLint, func(s *JobSpec) { s.Required = []string{"gpu"} }), capacity.ErrNoCapableTier, "", ErrNoCapableExecutor},
		{"toolchain mismatch", all(), spec(RequirementUnit, func(s *JobSpec) { s.ToolchainFP = "other" }), nil, ExecutorNode, nil},
		{"clean-room local", all(), spec(RequirementIntegration, noop), nil, ExecutorLocal, nil},
		{"docker required", all(), spec(RequirementArchitecture, func(s *JobSpec) { s.Required = []string{"clean-room", "docker"} }), nil, ExecutorNode, nil},
		{"untrusted source", RunnerSet{ExecutorLocal: &recRunner{}}, spec(RequirementLint, func(s *JobSpec) { s.SourceTrust = "" }), nil, "", ErrExternalSourceOnController},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fleet := &fakeFleet{leaseErr: c.leaseErr}
			rig := newResolverRig(t, c.runners, fleet, &fakeReader{exists: true})
			ex, err := rig.r.Resolve(context.Background(), c.spec)
			if c.wantErr != nil {
				if !errChainHas(err, c.wantErr) {
					t.Fatalf("Resolve = %+v, %v; want %v", ex, err, c.wantErr)
				}
				return
			}
			if err != nil || ex.Kind != c.want || ex.Env.RunnerKind != c.want {
				t.Fatalf("Resolve = %+v, %v; want %s", ex, err, c.want)
			}
			if (c.want == ExecutorNode) != (ex.Lease != nil) {
				t.Fatalf("lease = %v for %s; only the node kind holds one", ex.Lease, c.want)
			}
		})
	}
}

// TestExecutorSkipsUnregisteredKinds: with only the local runner, a docker
// job is ErrNoCapableExecutor, the local refusal names docker and the node
// and hosted steps are journaled as skipped.
func TestExecutorSkipsUnregisteredKinds(t *testing.T) {
	local := &recRunner{}
	rig := newResolverRig(t, RunnerSet{ExecutorLocal: local}, nil, nil)
	spec := testSpec(RequirementArchitecture)
	spec.Required = []string{"clean-room", "docker"}
	if _, err := rig.r.Resolve(context.Background(), spec); !errChainHas(err, ErrNoCapableExecutor) {
		t.Fatalf("Resolve = %v, want ErrNoCapableExecutor", err)
	}
	var skips []ExecutorKind
	for _, r := range rig.refusals {
		if strings.Contains(r.Reason, "runner is registered") {
			skips = append(skips, r.Step)
		}
		if r.JobID != "job-1" || r.Kind != RequirementArchitecture || r.At.IsZero() {
			t.Fatalf("refusal %+v lacks the job, kind or time", r)
		}
	}
	if len(rig.refusals) != 3 || rig.refusals[2].Step != ExecutorLocal || !strings.Contains(rig.refusals[2].Reason, "docker") ||
		len(skips) != 2 || skips[0] != ExecutorNode || skips[1] != ExecutorGitHubActions {
		t.Fatalf("refusals = %+v; want two skips, node and github-actions, then local (docker)", rig.refusals)
	}
	if local.n() != 0 || len(rig.gitCalls) != 0 {
		t.Fatalf("local runs %d, git calls %v; resolution must run nothing", local.n(), rig.gitCalls)
	}
}
