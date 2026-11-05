package ci

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// hostedRig drives a real Client against a scripted GitHub transport.
type hostedRig struct {
	runner                *GitHubActionsRunner
	client                *Client
	db                    *sql.DB
	store                 *jobs.CiAttestationStore
	clock                 *runtime.FixedClock
	env                   testOIDCEnv
	sj                    SubJob
	visibility            []HTTPResponse // one per read; the last one repeats
	dispatches, attention int
	refusals              []Refusal
	artifacts             HTTPResponse
	archive               []byte
	conclusion            RunConclusion // "" means the run never appears
}

func (h *hostedRig) Push(_ context.Context, item supervision.AttentionItem) (supervision.AttentionItem, error) {
	h.attention++
	return item, nil
}

func (h *hostedRig) Do(_ context.Context, req HTTPRequest) (HTTPResponse, error) {
	path := strings.TrimPrefix(req.URL, "https://api.github.com/repos/acme/widget")
	switch {
	case path == "":
		resp := h.visibility[0]
		if len(h.visibility) > 1 {
			h.visibility = h.visibility[1:]
		}
		return resp, nil
	case strings.HasSuffix(path, "/dispatches"):
		h.dispatches++
		return HTTPResponse{Status: 204}, nil
	case strings.HasPrefix(path, "/actions/runs/789/artifacts"):
		return h.artifacts, nil
	case path == "/actions/artifacts/101/zip":
		return HTTPResponse{Status: 200, Body: h.archive}, nil
	}
	return HTTPResponse{Status: 404}, nil
}

var (
	hostedPublic  = HTTPResponse{Status: 200, Body: []byte(`{"private":false,"visibility":"public"}`)}
	hostedPrivate = HTTPResponse{Status: 200, Body: []byte(`{"private":true,"visibility":"private"}`)}
)

func newHostedRig(t *testing.T) *hostedRig {
	t.Helper()
	ctx := context.Background()
	h := &hostedRig{db: openTestDB(t), env: newTestOIDCEnv(t), conclusion: ConclusionSuccess,
		visibility: []HTTPResponse{hostedPublic}, artifacts: HTTPResponse{Status: 200, Body: []byte(`{"total_count":0,"artifacts":[]}`)}}
	h.clock = runtime.NewFixedClock(h.env.now)
	for _, err := range []error{ApplyMigrationSchema(ctx, h.db, migrate.SQLiteEmitter{}, h.clock, "", ""),
		jobs.ApplyJobsSchema(ctx, h.db, migrate.SQLiteEmitter{}, h.clock, "", "")} {
		if err != nil {
			t.Fatal(err)
		}
	}
	h.store, _ = jobs.NewCiAttestationStore(h.db)
	commit := strings.Repeat("ab", 20)
	h.env.want = OIDCWant{RepositorySlug: "acme/widget", RepositoryID: "123", RepositoryOwnerID: "456",
		JobWorkflowRef: "acme/widget/.github/workflows/cascade-ci.yml@" + commit, CommitSHA: commit, RunID: "789"}
	h.sj = SubJob{Kind: RequirementLint, Snapshot: CandidateSnapshot{TreeHash: strings.Repeat("cd", 20), CheckpointID: "cp-1"},
		Ref: JobRef{JobID: "job-1", ProjectID: "p-1", RepoRoot: "/repo", CheckpointCommit: commit, Sensitivity: provider.SensitivityPublic,
			Lease: jobs.ResourceLease{RepoID: "repo-1"}}}
	pub, priv, _ := ed25519.GenerateKey(nil)
	allow := func() (bool, error) { return true, nil }
	h.client = NewClient(h, testEngine(t), "", 123, allowActionsRoutes)
	h.client.Allowed = allow
	var err error
	h.runner, err = NewGitHubActionsRunner(GitHubActionsDeps{
		Guard:  HostedGuard{Allowed: allow, Visibility: h.client, Clock: h.clock, Attention: h, Refusals: func(_ context.Context, r Refusal) { h.refusals = append(h.refusals, r) }},
		GitHub: h.client, CIDB: h.db, Store: h.store, JWKS: h.env.source, Self: nodes.Identity{NodeID: "controller", PubKey: pub},
		Sign: func(b []byte) ([]byte, error) { return ed25519.Sign(priv, b), nil },
		Repository: func(context.Context, string, string) (HostedRepository, error) {
			return HostedRepository{123, 456}, nil
		},
		Workflow:      func() (string, error) { return "cascade-ci.yml", nil },
		Origin:        func(context.Context, string) (string, error) { return "https://github.com/acme/widget.git", nil },
		PolicyVersion: func() string { return "policy-1" },
		Nonce:         func(context.Context, SubJob) (string, error) { return "nonce-1", nil },
		Wait:          h.wait,
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// wait is the poller: it records the dispatched run and advances the clock.
func (h *hostedRig) wait(ctx context.Context, d time.Duration) error {
	now := h.clock.Now()
	h.clock.Advance(d)
	if h.conclusion == "" {
		return nil
	}
	return Upsert(ctx, h.db, Run{RunID: 789, RepoID: 123, HeadSHA: h.env.want.CommitSHA, Status: RunStatusCompleted,
		Conclusion: h.conclusion, CreatedAt: now, UpdatedAt: now}, nil, nil)
}

func (h *hostedRig) run(t *testing.T) (SubJobResult, error) {
	t.Helper()
	return h.runner.Run(context.Background(), ResolvedExecutor{Kind: ExecutorGitHubActions}, h.sj)
}

// useToken publishes token through the fixture artifact listing.
func (h *hostedRig) useToken(t *testing.T, token string) {
	t.Helper()
	listing, err := os.ReadFile("testdata/github/artifacts.json")
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	f, _ := zw.Create("token.jwt")
	_, _ = f.Write([]byte(token))
	_ = zw.Close()
	h.artifacts, h.archive = HTTPResponse{Status: 200, Body: listing}, archive.Bytes()
}

// row returns the stored attestation; its ID is empty when there is none.
func (h *hostedRig) row(t *testing.T) jobs.CiAttestationRow {
	t.Helper()
	var id string
	_ = h.db.QueryRow(`SELECT id FROM ci_attestation`).Scan(&id)
	row, _, err := h.store.ByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func assertHostedRefused(t *testing.T, h *hostedRig, err error, condition string) {
	t.Helper()
	if !errChainHas(err, ErrHostedExecutorRefused) || !cascade.HasKind(err, cascade.KindPolicyDenied) || !strings.Contains(err.Error(), condition) {
		t.Fatalf("err = %v, want ErrHostedExecutorRefused naming %q", err, condition)
	}
	if h.dispatches != 0 || len(h.refusals) != 1 || h.attention != 1 || h.refusals[0].Step != ExecutorGitHubActions || h.row(t).ID != "" {
		t.Fatalf("dispatches=%d refusals=%v attention=%d row=%+v", h.dispatches, h.refusals, h.attention, h.row(t))
	}
}

// TestHostedExecutorRefusalMatrix walks every guard condition: each refusal
// is typed, names its condition, is journaled and raised, and dispatches nothing.
func TestHostedExecutorRefusalMatrix(t *testing.T) {
	origin := func(url string, err error) func(*hostedRig) {
		return func(h *hostedRig) {
			h.runner.d.Origin = func(context.Context, string) (string, error) { return url, err }
		}
	}
	allow := func(ok bool, err error) func(*hostedRig) {
		return func(h *hostedRig) { h.runner.d.Guard.Allowed = func() (bool, error) { return ok, err } }
	}
	visibility := func(reads ...HTTPResponse) func(*hostedRig) { return func(h *hostedRig) { h.visibility = reads } }
	for name, c := range map[string]struct {
		mutate    func(*hostedRig)
		condition string
	}{
		"allow unset":         {func(h *hostedRig) { h.runner.d.Guard.Allowed = nil }, "allow"},
		"allow false":         {allow(false, nil), "allow"},
		"allow unadmitted":    {allow(true, cascade.New(cascade.KindUnavailable, "not admitted")), "allow"},
		"private repo":        {visibility(hostedPrivate), "visibility"},
		"visibility error":    {visibility(HTTPResponse{Status: 503}), "visibility"},
		"visibility unknown":  {visibility(HTTPResponse{Status: 200, Body: []byte(`{}`)}), "visibility"},
		"no reader":           {func(h *hostedRig) { h.runner.d.Guard.Visibility = nil }, "visibility"},
		"internal data":       {func(h *hostedRig) { h.sj.Ref.Sensitivity = provider.SensitivityInternal }, "sensitivity"},
		"restricted data":     {func(h *hostedRig) { h.sj.Ref.Sensitivity = provider.SensitivityRestricted }, "sensitivity"},
		"not github":          {origin("https://gitlab.com/acme/widget.git", nil), "origin"},
		"lookalike host":      {origin("https://github.com.evil.test/acme/widget", nil), "origin"},
		"origin unreadable":   {origin("", cascade.New(cascade.KindUnavailable, "no remote")), "origin"},
		"private at dispatch": {visibility(hostedPublic, hostedPrivate), "dispatch"},
		"made private after resolution": {func(h *hostedRig) {
			h.visibility = []HTTPResponse{hostedPublic, hostedPrivate}
			if private, err := h.client.RepoVisibility(context.Background(), "acme", "widget"); private || err != nil {
				t.Fatalf("the resolver's read: private=%v err=%v", private, err)
			}
		}, "visibility"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHostedRig(t)
			c.mutate(h)
			_, err := h.run(t)
			assertHostedRefused(t, h, err, c.condition)
		})
	}
}

// countingResolver proves a refusal never re-enters resolution.
type countingResolver struct{ calls int }

func (r *countingResolver) Resolve(context.Context, JobSpec) (ResolvedExecutor, error) {
	r.calls++
	return ResolvedExecutor{Kind: ExecutorGitHubActions}, nil
}

// TestHostedExecutorRefusalNoFallthrough runs a refusal through the resolving
// executor: no other lane runs, the resolver is not asked again, and no
// non-pass terminal hides the refusal.
func TestHostedExecutorRefusalNoFallthrough(t *testing.T) {
	h := newHostedRig(t)
	h.visibility = []HTTPResponse{hostedPrivate}
	local, node, resolver := &recRunner{}, &recRunner{}, &countingResolver{}
	exec := NewResolvingSubJobExecutor(resolver, RunnerSet{ExecutorLocal: local, ExecutorNode: node, ExecutorGitHubActions: h.runner}, nil,
		ResolvingDeps{CIDB: h.db, Clock: h.clock, Fingerprint: func(context.Context) (string, error) { return testFP, nil }})
	res, err := exec.Run(context.Background(), h.sj)
	assertHostedRefused(t, h, err, "visibility")
	runs := -1
	_ = h.db.QueryRow(`SELECT count(*) FROM ` + tableRun).Scan(&runs)
	if res.Passed || resolver.calls != 1 || len(local.calls)+len(node.calls) != 0 || runs != 0 {
		t.Fatalf("res=%+v resolves=%d local=%d node=%d runs=%d", res, resolver.calls, len(local.calls), len(node.calls), runs)
	}
}

// hostedCountersign accepts all: only a record's own proof keeps it out of a gate.
var hostedCountersign = oidcCountersignVerifier(func(context.Context, attest.SignedAttestation) error { return nil })

// TestUnattestedNeverSatisfiesReceiveGate covers a missing token and a token
// that fails verification.
func TestUnattestedNeverSatisfiesReceiveGate(t *testing.T) {
	for name, token := range map[string]string{"no artifact": "", "invalid token": "invalid fixture payload"} {
		t.Run(name, func(t *testing.T) {
			h := newHostedRig(t)
			if token != "" {
				h.useToken(t, token)
			}
			res, err := h.run(t)
			row := h.row(t)
			if err != nil || res.Passed || row.AttestorClass != string(attest.ClassUnattested) || row.Signature != "" {
				t.Fatalf("res=%+v err=%v row=%+v", res, err, row)
			}
			sa := attest.SignedAttestation{CanonicalJSON: []byte(row.CanonicalJSON), Signature: row.Signature}
			if got := attest.DeriveClass(sa, nil, hostedCountersign); got.Eligible() {
				t.Fatalf("derived %q satisfies the gate", got)
			}
		})
	}
}

// TestHostedRunAttested proves a verified token yields one dispatch and a
// countersigned ephemeral-oidc record, and a failed run never passes.
func TestHostedRunAttested(t *testing.T) {
	for conclusion, passed := range map[RunConclusion]bool{ConclusionSuccess: true, ConclusionFailure: false} {
		t.Run(string(conclusion), func(t *testing.T) {
			h := newHostedRig(t)
			h.useToken(t, h.env.signToken(t, h.env.validClaims(), "", ""))
			h.conclusion = conclusion
			res, err := h.run(t)
			if err != nil || res.Passed != passed || res.RunID != 789 || res.RepoID != 123 || h.dispatches != 1 {
				t.Fatalf("res=%+v err=%v dispatches=%d", res, err, h.dispatches)
			}
			if res.Env != (Environment{RunnerKind: ExecutorGitHubActions, IsolationClass: isolationHosted, Ephemeral: true}) {
				t.Fatalf("env = %+v", res.Env)
			}
			row := h.row(t)
			sa := attest.SignedAttestation{CanonicalJSON: []byte(row.CanonicalJSON), Signature: row.Signature}
			if row.AttestorClass != string(attest.ClassEphemeralOIDC) || attest.DeriveClass(sa, nil, hostedCountersign) != attest.ClassEphemeralOIDC {
				t.Fatalf("row = %+v", row)
			}
		})
	}
}

// TestHostedRunTimeout proves a run from before the dispatch never correlates.
func TestHostedRunTimeout(t *testing.T) {
	h := newHostedRig(t)
	h.conclusion = ""
	stale := h.clock.Now().Add(-time.Hour)
	run := Run{RunID: 788, RepoID: 123, HeadSHA: h.env.want.CommitSHA, Status: RunStatusCompleted, Conclusion: ConclusionSuccess, CreatedAt: stale, UpdatedAt: stale}
	if err := Upsert(context.Background(), h.db, run, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := h.run(t); !errChainHas(err, ErrHostedRunTimeout) || !cascade.HasKind(err, cascade.KindTimeout) || h.row(t).ID != "" {
		t.Fatalf("err = %v, row = %+v", err, h.row(t))
	}
}
