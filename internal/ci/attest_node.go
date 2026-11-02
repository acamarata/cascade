package ci

// Purpose: the node half of ci.attest.sign (contract:node-attest-sign): a
// node runs CI through NodeAttestor.Execute, which binds the run to the
// commit and checkout it ran, and NodeAttestor.Sign later builds the
// attestation record from that run alone and signs it once with the node
// identity key.
// Inputs: a request that internal/nodes already verified against the pinned
// controller key; the node's own ci_results rows and checkout.
// Outputs: the encoded attest.SignedAttestation.
// Constraints: exit status, result hashes, commit and tree never come from
// the request; an unknown or incomplete run is KindPermissionDenied; a second
// sign of one run is KindConflict; the private key stays in NodeKeystore.
// SPORT: internal.ci.NodeAttestor/ADDED (P1-CI-13).

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// runnerKindNode is the Environment.RunnerKind of a node-built record.
const runnerKindNode = "node"

// NodeAttestor runs and signs CI on an enrolled node.
type NodeAttestor struct {
	self  nodes.Identity
	keys  *nodes.NodeKeystore
	db    *sql.DB
	runs  *jobs.CiAttestSignedStore
	clock runtime.Clock
}

// NewNodeAttestor binds the node identity, its keystore and the node-local
// store (ci_results plus the jobs schema).
func NewNodeAttestor(self nodes.Identity, keys *nodes.NodeKeystore, db *sql.DB, clock runtime.Clock) (*NodeAttestor, error) {
	if self.NodeID == "" || len(self.PubKey) != 32 || keys == nil || db == nil || clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "ci: node attestor requires identity, keystore, store and clock")
	}
	runs, err := jobs.NewCiAttestSignedStore(db)
	if err != nil {
		return nil, err
	}
	return &NodeAttestor{self: self, keys: keys, db: db, runs: runs, clock: clock}, nil
}

// OpenNodeAttestor opens the node's own store, <dataDir>/cascade.db with the
// ci_results and jobs schemas applied, and binds a NodeAttestor over it. The
// returned close releases the store.
func OpenNodeAttestor(ctx context.Context, self nodes.Identity, keys *nodes.NodeKeystore, dataDir string, clock runtime.Clock) (*NodeAttestor, func() error, error) {
	db, err := openResultsDB(ctx, dataDir, clock)
	if err != nil {
		return nil, nil, err
	}
	dbPath := filepath.Join(dataDir, "cascade.db")
	if err := jobs.ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, clock, dbPath, filepath.Join(dataDir, "backups")); err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	a, err := NewNodeAttestor(self, keys, db, clock)
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return a, db.Close, nil
}

// Execute runs cfg on the node store (Execute over a.db) and binds the run
// to executionID, the HEAD commit of cfg.RepoRoot and that checkout.
func (a *NodeAttestor) Execute(ctx context.Context, deps Deps, cfg RunnerConfig, executionID string, runID, repoID int64, repoName string) (RunResult, error) {
	if executionID == "" || cfg.RepoRoot == "" {
		return RunResult{}, cascade.New(cascade.KindInvalidInput, "ci: a node run needs an execution id and a checkout")
	}
	commit, err := streamGit(ctx, cfg.RepoRoot, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return RunResult{}, cascade.Wrap(cascade.KindUnavailable, err, "ci: resolve the checkout commit")
	}
	deps.DB = a.db
	res, err := Execute(ctx, deps, cfg, runID, repoID, repoName)
	if err != nil {
		return res, err
	}
	return res, a.runs.RecordRun(ctx, jobs.NodeRun{ExecutionID: executionID, RunID: runID, RepoID: repoID,
		CommitSHA: strings.TrimSpace(commit), Checkout: cfg.RepoRoot, RecordedAt: a.clock.Now().Unix()})
}

// Sign builds the record of run req.ExecutionID from the node's own rows and
// signs it with the node identity key. It is the nodes.AttestSigner composed
// into node serve.
func (a *NodeAttestor) Sign(ctx context.Context, req nodes.AttestSignRequest) (json.RawMessage, error) {
	now := a.clock.Now()
	if !req.ExpiresAt.After(now) {
		return nil, cascade.New(cascade.KindPermissionDenied, "ci: attest-sign request has expired")
	}
	if _, err := nodes.AttestSignPayload(a.self.NodeID, req); err != nil {
		return nil, err
	}
	run, ok, err := a.runs.Run(ctx, req.ExecutionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, cascade.New(cascade.KindPermissionDenied, "ci: no run of this node has that execution id")
	}
	rec, err := a.record(ctx, run, req)
	if err != nil {
		return nil, err
	}
	sa, err := attest.Attest(rec, func(p []byte) ([]byte, error) { return a.keys.Sign(ctx, a.self.NodeID, p) })
	if err != nil {
		return nil, err
	}
	if err := a.runs.MarkSigned(ctx, req.ExecutionID, req.Nonce, now.Unix()); err != nil {
		return nil, err
	}
	out, err := json.Marshal(sa)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "ci: encode signed attestation")
	}
	return out, nil
}

// record reads the terminal run and builds its record; only the identifiers
// come from req.
func (a *NodeAttestor) record(ctx context.Context, run jobs.NodeRun, req nodes.AttestSignRequest) (attest.AttestationRecord, error) {
	var status, conclusion string
	var started, ended int64
	err := a.db.QueryRowContext(ctx, `SELECT status, conclusion, created_at, updated_at FROM `+tableRun+` WHERE run_id=? AND repo_id=?`,
		run.RunID, run.RepoID).Scan(&status, &conclusion, &started, &ended)
	if errors.Is(err, sql.ErrNoRows) {
		return attest.AttestationRecord{}, cascade.New(cascade.KindPermissionDenied, "ci: the bound run has no result on this node")
	}
	if err != nil {
		return attest.AttestationRecord{}, cascade.Wrap(cascade.KindUnavailable, err, "ci: read node run")
	}
	source, err := runSource(ctx, a.db, run.RunID, run.RepoID)
	if err != nil {
		return attest.AttestationRecord{}, err
	}
	if status != string(RunStatusCompleted) || source != SourceLocal {
		return attest.AttestationRecord{}, cascade.New(cascade.KindPermissionDenied, "ci: the bound run is not a completed local run")
	}
	hashes, names, failed, err := a.stepHashes(ctx, run)
	if err != nil {
		return attest.AttestationRecord{}, err
	}
	tree, err := streamGit(ctx, run.Checkout, "rev-parse", "--verify", run.CommitSHA+"^{tree}")
	if err != nil {
		return attest.AttestationRecord{}, cascade.Wrap(cascade.KindUnavailable, err, "ci: read the run's tree")
	}
	exit := 0
	if failed || conclusion != string(ConclusionSuccess) {
		exit = 1
	}
	return attest.AttestationRecord{SchemaVersion: attest.AttestationSchemaVersion, AttestorClass: attest.ClassEnrolledNode,
		CheckpointID: req.CheckpointID, CommitSHA: run.CommitSHA, TreeHash: strings.TrimSpace(tree),
		RepoID: req.RepoID, JobSpecHash: req.JobSpecHash, PolicyVersion: req.PolicyVersion,
		Nonce: req.Nonce, ExpiresAt: req.ExpiresAt, NodeID: a.self.NodeID, KeyID: nodes.KeyIDFor(a.self.PubKey),
		Environment: attest.Environment{RunnerKind: runnerKindNode, NodeID: a.self.NodeID},
		ExitStatus:  exit, ResultHashes: hashes, TargetSelection: strings.Join(names, ","),
		StartedAt: time.UnixMilli(started), EndedAt: time.UnixMilli(ended)}, nil
}

// stepHashes hashes every step of the run's terminal job: "<step>:<sha256>".
// A run with no job, an unfinished job or no step is not signable.
func (a *NodeAttestor) stepHashes(ctx context.Context, run jobs.NodeRun) (hashes, names []string, failed bool, err error) {
	var jobCount, incomplete int
	err = a.db.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(CASE WHEN status!='completed' THEN 1 ELSE 0 END),0)
		FROM `+tableJob+` WHERE run_id=?`, run.RunID).Scan(&jobCount, &incomplete)
	if err != nil {
		return nil, nil, false, cascade.Wrap(cascade.KindUnavailable, err, "ci: read node run jobs")
	}
	if jobCount == 0 || incomplete > 0 {
		return nil, nil, false, cascade.New(cascade.KindPermissionDenied, "ci: the bound run's jobs are not terminal")
	}
	rows, err := a.db.QueryContext(ctx, `SELECT s.number, s.name, s.status, s.conclusion FROM `+tableStep+` s
		JOIN `+tableJob+` j ON j.job_id = s.job_id WHERE j.run_id=? ORDER BY s.job_id, s.number`, run.RunID)
	if err != nil {
		return nil, nil, false, cascade.Wrap(cascade.KindUnavailable, err, "ci: read node run steps")
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var number int
		var name, status, conclusion string
		if err := rows.Scan(&number, &name, &status, &conclusion); err != nil {
			return nil, nil, false, cascade.Wrap(cascade.KindUnavailable, err, "ci: scan node run step")
		}
		raw, _ := json.Marshal([]any{run.RunID, run.RepoID, run.CommitSHA, number, name, status, conclusion})
		sum := sha256.Sum256(raw)
		hashes, names = append(hashes, name+":"+hex.EncodeToString(sum[:])), append(names, name)
		failed = failed || status != string(RunStatusCompleted) || conclusion != string(ConclusionSuccess)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, cascade.Wrap(cascade.KindUnavailable, err, "ci: iterate node run steps")
	}
	if len(hashes) == 0 {
		return nil, nil, false, cascade.New(cascade.KindPermissionDenied, "ci: the bound run recorded no step")
	}
	return hashes, names, failed, nil
}
