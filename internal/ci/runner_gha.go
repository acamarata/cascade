// Purpose: HostedGuard and GitHubActionsRunner (P1-NODE-09). Run checks the
// guard, dispatches the workflow, correlates the run and stores its record.
// Inputs: admitted ci.hosted.allow, live visibility, ci_results rows and the
// cascade-oidc run artifact, whose contents are untrusted.
// Outputs: a SubJobResult and one ci_attestation row.
// Constraints: fail closed. ErrHostedExecutorRefused ends the requirement: no
// other lane runs and the resolver is not re-entered. Only CountersignOIDC
// produces ephemeral-oidc; any other record is unattested and never a pass.
// The guard itself lives in runner_gha_guard.go.
// SPORT: internal.ci.GitHubActionsRunner/ADDED.

package ci

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The correlation window and the cascade-oidc artifact's shape.
const (
	hostedPollWindow    = 30 * time.Minute
	hostedPollEvery     = 15 * time.Second
	hostedTokenArtifact = "cascade-oidc"
	hostedTokenFile     = "token.jwt"
	maxHostedTokenBytes = 64 << 10
)

// ErrHostedRunTimeout reports no finished run inside the poll window.
var ErrHostedRunTimeout = cascade.New(cascade.KindTimeout, "ci: no finished hosted run inside the poll window")

// HostedGitHub is the part of *Client a hosted run writes and fetches through.
type HostedGitHub interface {
	TriggerWorkflowDispatch(ctx context.Context, owner, repo, workflow, ref string) error
	ListRunArtifacts(ctx context.Context, owner, repo string, runID int64) ([]Artifact, error)
	DownloadArtifact(ctx context.Context, owner, repo string, artifactID int64) ([]byte, error)
}

// HostedRepository is a repository's numeric identity: ID keys its ci_run rows
// and both ids bind the OIDC token.
type HostedRepository struct{ ID, OwnerID int64 }

// GitHubActionsDeps carries every runner input; Guard.Clock is the runner's clock.
type GitHubActionsDeps struct {
	Guard         HostedGuard
	GitHub        HostedGitHub
	Repository    func(ctx context.Context, owner, repo string) (HostedRepository, error)
	Workflow      func() (string, error) // the admitted ci.hosted.workflow
	Origin        func(ctx context.Context, repoRoot string) (string, error)
	CIDB          *sql.DB
	Store         *jobs.CiAttestationStore
	JWKS          JWKSource
	Sign          func([]byte) ([]byte, error) // the controller key
	Self          nodes.Identity               // the controller identity
	PolicyVersion func() string
	Nonce         func(ctx context.Context, sj SubJob) (string, error)
	Wait          func(ctx context.Context, d time.Duration) error
}

// GitHubActionsRunner runs one sub-job as a workflow_dispatch run.
type GitHubActionsRunner struct{ d GitHubActionsDeps }

// NewGitHubActionsRunner validates d and returns the runner.
func NewGitHubActionsRunner(d GitHubActionsDeps) (*GitHubActionsRunner, error) {
	if d.Guard.Clock == nil || d.GitHub == nil || d.Origin == nil || d.Wait == nil || d.Repository == nil || d.Workflow == nil ||
		d.CIDB == nil || d.Store == nil || d.JWKS == nil || d.Sign == nil || d.PolicyVersion == nil || d.Nonce == nil || d.Self.NodeID == "" || len(d.Self.PubKey) != 32 {
		return nil, cascade.New(cascade.KindInvalidInput, "ci: NewGitHubActionsRunner is missing a dependency")
	}
	return &GitHubActionsRunner{d: d}, nil
}

// Run implements Runner. Any refusal fails the requirement closed.
func (r *GitHubActionsRunner) Run(ctx context.Context, ex ResolvedExecutor, sj SubJob) (SubJobResult, error) {
	commit := sj.Ref.CheckpointCommit
	if ex.Kind != ExecutorGitHubActions || commit == "" || sj.Snapshot.TreeHash == "" || sj.Snapshot.CheckpointID == "" {
		return SubJobResult{}, cascade.New(cascade.KindInvalidInput, "ci: a hosted run needs its executor, commit, tree and checkpoint")
	}
	spec := JobSpec{Kind: sj.Kind, Sensitivity: sj.Ref.Sensitivity, CheckpointCommit: commit, RepoRoot: sj.Ref.RepoRoot,
		JobID: sj.Ref.JobID, ProjectID: sj.Ref.ProjectID, RepoID: sj.Ref.Lease.RepoID}
	origin, err := r.d.Origin(ctx, sj.Ref.RepoRoot)
	if err != nil {
		origin = "" // the guard refuses an unreadable origin
	}
	owner, repo, err := r.d.Guard.Check(ctx, spec, origin)
	if err != nil {
		return SubJobResult{}, err
	}
	identity, workflow, nonce, err := r.inputs(ctx, sj, owner, repo)
	if err != nil {
		return SubJobResult{}, err
	}
	dispatched := r.d.Guard.Clock.Now()
	if err := r.d.GitHub.TriggerWorkflowDispatch(ctx, owner, repo, workflow, commit); err != nil {
		if errors.Is(err, ErrHostedExecutorRefused) { // same Kind: the dispatch's own live re-check refused
			return SubJobResult{}, r.d.Guard.refuse(ctx, spec, "dispatch: "+err.Error())
		}
		return SubJobResult{}, err
	}
	run, err := r.correlate(ctx, identity.ID, commit, dispatched)
	if err != nil {
		return SubJobResult{}, err
	}
	want := OIDCWant{RepositorySlug: owner + "/" + repo, RepositoryID: strconv.FormatInt(identity.ID, 10),
		RepositoryOwnerID: strconv.FormatInt(identity.OwnerID, 10), CommitSHA: commit, RunID: strconv.FormatInt(run.RunID, 10),
		JobWorkflowRef: fmt.Sprintf("%s/%s/.github/workflows/%s@%s", owner, repo, workflow, commit)}
	token, err := r.token(ctx, owner, repo, run.RunID)
	if err != nil {
		return SubJobResult{}, err
	}
	sa, err := r.attestation(ctx, r.record(ex, sj, run, nonce), token, want)
	if err != nil {
		return SubJobResult{}, err
	}
	row := localAttestationRow(sa, r.d.Guard.Clock.Now())
	row.AttestorClass = string(sa.Record.AttestorClass)
	if err := r.d.Store.Insert(ctx, row); err != nil && !errChainHas(err, jobs.ErrAlreadyAttested) { // a redispatch keeps its first row
		return SubJobResult{}, err
	}
	return SubJobResult{RunID: run.RunID, RepoID: identity.ID, ExecutorKind: string(ExecutorGitHubActions),
		Passed: sa.Record.ExitStatus == 0 && sa.Record.AttestorClass.Eligible(),
		Env:    Environment{RunnerKind: ExecutorGitHubActions, IsolationClass: isolationHosted, Ephemeral: true, ToolchainFP: ex.Env.ToolchainFP}}, nil
}

// inputs reads everything a dispatch needs, so a failure dispatches nothing.
func (r *GitHubActionsRunner) inputs(ctx context.Context, sj SubJob, owner, repo string) (id HostedRepository, workflow, nonce string, err error) {
	if id, err = r.d.Repository(ctx, owner, repo); err != nil || id.ID <= 0 || id.OwnerID <= 0 {
		return id, "", "", errors.Join(err, cascade.Newf(cascade.KindIntegrity, "ci: %s/%s has no numeric identity", owner, repo))
	}
	if workflow, err = r.d.Workflow(); err != nil {
		return id, "", "", err
	}
	nonce, err = r.d.Nonce(ctx, sj)
	return id, workflow, nonce, err
}

// correlate waits for the newest completed run of commit since the dispatch.
// GitHub's created_at has whole seconds; negative run ids are local. Wait paces
// the loop and is where production refreshes ci_results.
func (r *GitHubActionsRunner) correlate(ctx context.Context, repoID int64, commit string, dispatched time.Time) (Run, error) {
	deadline := dispatched.Add(hostedPollWindow)
	for {
		var status, conclusion string
		var created, updated int64
		run := Run{RepoID: repoID, HeadSHA: commit}
		err := r.d.CIDB.QueryRowContext(ctx, `SELECT run_id, status, conclusion, created_at, updated_at FROM `+tableRun+`
			WHERE repo_id = ? AND head_sha = ? AND created_at >= ? AND run_id > 0 ORDER BY created_at DESC, run_id DESC LIMIT 1`,
			repoID, commit, millis(dispatched.Truncate(time.Second))).Scan(&run.RunID, &status, &conclusion, &created, &updated)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Run{}, cascade.Wrap(cascade.KindUnavailable, err, "ci: read the hosted run")
		}
		if err == nil && status == string(RunStatusCompleted) {
			run.Status, run.Conclusion = RunStatusCompleted, RunConclusion(conclusion)
			run.CreatedAt, run.UpdatedAt = time.UnixMilli(created), time.UnixMilli(updated)
			return run, nil
		}
		if !r.d.Guard.Clock.Now().Before(deadline) {
			return Run{}, cascade.Wrapf(cascade.KindTimeout, ErrHostedRunTimeout, "ci: no finished run of %s after %s", commit, hostedPollWindow)
		}
		if err := r.d.Wait(ctx, hostedPollEvery); err != nil {
			return Run{}, err
		}
	}
}

// token returns the run's token.jwt, or "" when it has none. A transport
// failure is an error; an unreadable archive is no token.
func (r *GitHubActionsRunner) token(ctx context.Context, owner, repo string, runID int64) (string, error) {
	artifacts, err := r.d.GitHub.ListRunArtifacts(ctx, owner, repo, runID)
	for _, a := range artifacts {
		if err == nil && a.Name == hostedTokenArtifact && !a.Expired {
			archive, derr := r.d.GitHub.DownloadArtifact(ctx, owner, repo, a.ID)
			return tokenFromArchive(archive), derr
		}
	}
	return "", err
}

// tokenFromArchive reads token.jwt from a ZIP, capped at 64 KiB.
func tokenFromArchive(archive []byte) string {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return ""
	}
	f, err := zr.Open(hostedTokenFile)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxHostedTokenBytes+1))
	if err != nil || len(raw) > maxHostedTokenBytes {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// record builds the unattested record of a finished run.
func (r *GitHubActionsRunner) record(ex ResolvedExecutor, sj SubJob, run Run, nonce string) attest.AttestationRecord {
	exit := 1
	if run.Conclusion == ConclusionSuccess {
		exit = 0
	}
	digest := func(v any) string {
		raw, _ := json.Marshal(v)
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:])
	}
	return attest.AttestationRecord{SchemaVersion: attest.AttestationSchemaVersion, AttestorClass: attest.ClassUnattested,
		CheckpointID: sj.Snapshot.CheckpointID, CommitSHA: run.HeadSHA, TreeHash: sj.Snapshot.TreeHash, RepoID: sj.Ref.Lease.RepoID,
		JobSpecHash: digest([]any{sj.Kind, run.HeadSHA, sj.Snapshot.TreeHash, ex.Env.ToolchainFP}), PolicyVersion: r.d.PolicyVersion(),
		Nonce: nonce, ExpiresAt: run.CreatedAt.Add(attest.MaxAttestationLifetime), NodeID: r.d.Self.NodeID, KeyID: nodes.KeyIDFor(r.d.Self.PubKey),
		Environment: attest.Environment{RunnerKind: string(ExecutorGitHubActions), IsolationClass: isolationHosted, Ephemeral: true, ToolchainFP: ex.Env.ToolchainFP},
		ExitStatus:  exit, TargetSelection: string(sj.Kind), ToolchainFP: ex.Env.ToolchainFP, StartedAt: run.CreatedAt, EndedAt: run.UpdatedAt,
		ResultHashes: []string{string(sj.Kind) + ":" + digest([]any{run.RunID, run.RepoID, run.HeadSHA, run.Status, run.Conclusion})}}
}

// attestation countersigns rec when token verifies; otherwise rec stays
// unattested and unsigned. A signing failure is an error, never a downgrade.
func (r *GitHubActionsRunner) attestation(ctx context.Context, rec attest.AttestationRecord, token string, want OIDCWant) (attest.SignedAttestation, error) {
	if token != "" {
		sa, err := CountersignOIDC(ctx, rec, token, want, r.d.JWKS, r.d.Sign, r.d.Guard.Clock.Now())
		var invalid *ErrOIDCInvalid
		if err == nil || !errors.As(err, &invalid) {
			return sa, err
		}
	}
	canon, err := attest.CanonicalJSON(rec)
	return attest.SignedAttestation{Record: rec, CanonicalJSON: canon}, err
}
