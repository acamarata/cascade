//go:build !windows

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/ci/attest"
	contextgit "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ciAcceptanceVerifier runs acceptance on the daemon's recorded checkpoint.
type ciAcceptanceVerifier struct {
	w           *daemonWiring
	jobsDB      *sql.DB
	registry    *jobs.RepoRegistry
	mu          sync.Mutex
	once        sync.Once
	attestor    *ci.LocalAttestor
	trust       attest.TrustStore
	identityErr error
	issued      map[string]acceptanceNonce
	dispatcher  *ci.Dispatcher
	db          *sql.DB
	clock       runtime.Clock
	model       func(string) ci.RequirementModel
	terminal    chan struct{}
	ticker      func() runtime.Ticker
}

// acceptanceNonceTTL leaves headroom under the attestation lifetime cap: a
// re-run that started before the nonce was issued still signs inside it.
const acceptanceNonceTTL = attest.MaxAttestationLifetime - time.Hour

type acceptanceNonce struct {
	value   string
	expires time.Time
}

func newCIAcceptanceVerifier(w *daemonWiring) (*ciAcceptanceVerifier, error) {
	if w.Deps.CI == nil {
		return nil, cascade.New(cascade.KindUnavailable, "completion-gate: CI runtime unavailable")
	}
	v := &ciAcceptanceVerifier{
		w: w, jobsDB: w.Deps.Fleet.JobsDB(), issued: make(map[string]acceptanceNonce),
		dispatcher: w.Deps.CI.Dispatcher(), db: w.Deps.CI.CIDB(), clock: w.Clock,
		model: w.Deps.CI.Model, terminal: make(chan struct{}, 1),
		ticker: func() runtime.Ticker { return runtime.NewSystemTicker(time.Second) },
	}
	if err := jobs.ApplyRepoRegistrySchema(w.Ctx, v.jobsDB, migrate.SQLiteEmitter{}, w.Clock, "", ""); err != nil {
		return nil, err
	}
	var err error
	v.registry, err = jobs.NewRepoRegistry(v.jobsDB, scope.NewGraphStore(v.jobsDB), contextgit.GitCommonDir, w.Clock)
	if err != nil {
		return nil, err
	}
	v.dispatcher.OnTerminal(func(ev ci.CIResultEvent) {
		if ev.Acceptance {
			select {
			case v.terminal <- struct{}{}:
			default:
			}
		}
	})
	return v, nil
}

// VerifyAcceptance waits for the full exact-checkpoint acceptance re-run.
func (v *ciAcceptanceVerifier) VerifyAcceptance(ctx context.Context, jobID string) (jobs.AcceptanceVerdict, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	snap, found, err := v.dispatcher.CurrentCheckpoint(ctx, jobID)
	if err != nil || !found {
		return jobs.AcceptanceVerdict{Reason: "no current checkpoint"}, err
	}
	ref, err := v.checkpointRef(ctx, snap)
	if err != nil {
		return jobs.AcceptanceVerdict{}, err
	}
	return v.attestedRerun(ctx, ref, snap)
}

func (v *ciAcceptanceVerifier) attestedRerun(ctx context.Context, ref ci.JobRef, snap ci.CandidateSnapshot) (jobs.AcceptanceVerdict, error) {
	model := v.model(ref.RepoRoot)
	plan := ci.AcceptancePlan(ctx, ref, model, snap)
	nonce, err := v.issue(ctx, ref, snap, plan)
	if err != nil {
		return jobs.AcceptanceVerdict{}, err
	}
	if verdict, found, err := v.storedVerdict(ctx, snap, nonce); found || err != nil {
		return verdict, err
	}
	verdict, err := v.wait(ctx, func(ctx context.Context) (ci.AcceptanceResult, error) {
		res, err := ciAcceptanceRerun(v.dispatcher, ctx, ref, model)
		if err != nil {
			return res, err
		}
		v.once.Do(func() { v.identityErr = v.ensureAttestor(ctx) })
		if v.identityErr != nil {
			return res, v.identityErr
		}
		_, err = v.attestor.AttestRerun(ctx, res, ref, nonce.value, nonce.expires)
		return res, err
	})
	if err == nil {
		verdict, _, err = v.storedVerdict(ctx, snap, nonce)
	}
	return verdict, err
}

func (v *ciAcceptanceVerifier) issue(ctx context.Context, ref ci.JobRef, snap ci.CandidateSnapshot, plan ci.CIRequirementPlan) (acceptanceNonce, error) {
	repoID, err := v.registry.ByPath(ctx, ref.RepoRoot)
	if err != nil {
		return acceptanceNonce{}, err
	}
	if nonce, ok := v.issued[snap.CheckpointID]; ok {
		return nonce, acceptanceNonceRepo(ctx, v.jobsDB, v.clock, nonce.value, repoID)
	}
	var prior acceptanceNonce
	var storedExpires int64
	err = v.jobsDB.QueryRowContext(ctx, `SELECT nonce, expires_at FROM attestation_nonce WHERE job_id=? AND checkpoint_id=? ORDER BY issued_at LIMIT 1`, ref.JobID, snap.CheckpointID).Scan(&prior.value, &storedExpires)
	if err == nil {
		prior.expires = time.Unix(storedExpires, 0).UTC()
		v.issued[snap.CheckpointID] = prior
		return prior, acceptanceNonceRepo(ctx, v.jobsDB, v.clock, prior.value, repoID)
	}
	if err != sql.ErrNoRows {
		return prior, cascade.Wrap(cascade.KindUnavailable, err, "acceptance: read issued nonce")
	}
	store := jobs.NewStore(v.jobsDB)
	job, ok, err := store.GetJob(ctx, ref.JobID)
	if err != nil {
		return acceptanceNonce{}, err
	}
	if !ok {
		return acceptanceNonce{}, cascade.New(cascade.KindNotFound, "acceptance job missing")
	}
	deps, err := store.Dependencies(ctx, job.ID)
	if err != nil {
		return acceptanceNonce{}, err
	}
	kinds := []string{}
	for _, kind := range []ci.RequirementKind{ci.RequirementFormat, ci.RequirementLint, ci.RequirementCompile, ci.RequirementUnit, ci.RequirementIntegration, ci.RequirementArchitecture, ci.RequirementSecurity} {
		if plan.Requirement.Requires(kind) {
			kinds = append(kinds, string(kind))
		}
	}
	nonces, _ := jobs.NewAttestationNonceStore(v.jobsDB, v.clock)
	value, expires, err := nonces.Issue(ctx, job.ID, snap.CheckpointID, repoID, jobs.JobSpecHash(job, deps), "ci-acceptance-v1", kinds, acceptanceNonceTTL)
	if err != nil {
		return acceptanceNonce{}, err
	}
	nonce := acceptanceNonce{value, expires}
	v.issued[snap.CheckpointID] = nonce
	return nonce, nil
}

func (v *ciAcceptanceVerifier) ensureAttestor(ctx context.Context) error {
	w := v.w
	constructor := w.Deps.Daemon.NewKeystore
	if constructor == nil {
		constructor = nodes.NewNodeKeystore
	}
	keys, err := constructor(nodeKeystoreConfig(w.Deps.Daemon.SecretsDir, w.Paths.DataDir()))
	if err != nil {
		return err
	}
	self, err := nodes.EnsureLocalIdentity(ctx, nodes.NewFileSelfIdentityBackend(w.Paths.DataDir()), keys, rand.Reader)
	if err != nil {
		return err
	}
	v.trust = attest.NewControllerTrustStore(self, attest.NewRecordTrustStore(nodes.NewRecordStore(nodes.NewFileRecordBackend(w.Paths.DataDir()), v.clock)))
	store, _ := jobs.NewCiAttestationStore(v.jobsDB)
	v.attestor, err = ci.NewLocalAttestor(self, func(p []byte) ([]byte, error) { return keys.Sign(w.Ctx, self.NodeID, p) }, store, v.trust, func() string { return "ci-acceptance-v1" }, v.clock)
	if err != nil {
		return err
	}
	ledger, _, err := completionLedger(ctx, w.Store, w.Clock, w.Paths, w.Deps.Fleet.Leases(), completionController(w.Paths))
	if err != nil {
		return err
	}
	v.attestor.BindSources(v.db, ledger)
	return nil
}

func (v *ciAcceptanceVerifier) storedVerdict(ctx context.Context, snap ci.CandidateSnapshot, nonce acceptanceNonce) (jobs.AcceptanceVerdict, bool, error) {
	var id string
	err := v.jobsDB.QueryRowContext(ctx, `SELECT id FROM ci_attestation WHERE nonce=?`, nonce.value).Scan(&id)
	if err == sql.ErrNoRows {
		return jobs.AcceptanceVerdict{}, false, nil
	}
	if err != nil {
		return jobs.AcceptanceVerdict{}, false, err
	}
	v.once.Do(func() { v.identityErr = v.ensureAttestor(ctx) })
	if v.identityErr != nil {
		return jobs.AcceptanceVerdict{}, true, v.identityErr
	}
	store, _ := jobs.NewCiAttestationStore(v.jobsDB)
	row, ok, err := store.ByID(ctx, id)
	if err != nil || !ok {
		return jobs.AcceptanceVerdict{}, true, err
	}
	sa := attest.SignedAttestation{CanonicalJSON: []byte(row.CanonicalJSON), Signature: row.Signature}
	err = attest.VerifySignature(ctx, sa, row.CommitSHA, snap.TreeHash, v.trust, v.clock.Now())
	if err != nil {
		return jobs.AcceptanceVerdict{}, true, err
	}
	nonces, _ := jobs.NewAttestationNonceStore(v.jobsDB, v.clock)
	fact, _, err := nonces.Issued(ctx, nonce.value)
	var evidence bool
	if readErr := v.jobsDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM jobs_evidence WHERE job_id=? AND artifact_ref=?)`, fact.JobID, "ci_attestation:"+id).Scan(&evidence); readErr != nil || !evidence {
		return jobs.AcceptanceVerdict{}, true, cascade.New(cascade.KindIntegrity, "acceptance: attestation evidence missing")
	}
	record, decodeErr := attest.DecodeCanonical(sa.CanonicalJSON)
	if decodeErr != nil {
		return jobs.AcceptanceVerdict{}, true, decodeErr
	}
	return jobs.AcceptanceVerdict{Passed: record.Passing(fact.PlanKinds), CheckpointID: snap.CheckpointID, TreeHash: snap.TreeHash, AttestationNonce: nonce.value}, true, err
}

// AttestationTrustStore provides the completion gate the composed current trust.
func (v *ciAcceptanceVerifier) AttestationTrustStore() attest.TrustStore { return v }

// Lookup resolves an attesting key through the controller and enrolled records.
func (v *ciAcceptanceVerifier) Lookup(ctx context.Context, nodeID, keyID string) (ed25519.PublicKey, error) {
	if v.trust == nil {
		return nil, attest.ErrUnknownNode
	}
	return v.trust.Lookup(ctx, nodeID, keyID)
}

// ciAcceptanceRerun permits tests to replace commit-tree resolution.
var ciAcceptanceRerun = (*ci.Dispatcher).AcceptanceRerun

func (v *ciAcceptanceVerifier) checkpointRef(ctx context.Context, snap ci.CandidateSnapshot) (ci.JobRef, error) {
	var raw string
	if err := v.db.QueryRowContext(ctx, `SELECT dispatches FROM ci_stream_attempt WHERE attempt_id = ?`, snap.AttemptID).Scan(&raw); err != nil {
		return ci.JobRef{}, cascade.Wrap(cascade.KindUnavailable, err, "completion-gate: read checkpoint dispatch")
	}
	var entries []struct {
		Ref ci.JobRef `json:"ref"`
	}
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return ci.JobRef{}, cascade.Wrap(cascade.KindIntegrity, err, "completion-gate: decode checkpoint dispatch")
	}
	if len(entries) == 0 {
		return ci.JobRef{}, cascade.New(cascade.KindUnavailable, "completion-gate: checkpoint has no dispatch")
	}
	return entries[0].Ref, nil
}

type ciAcceptanceOutcome struct {
	result ci.AcceptanceResult
	err    error
}

func (v *ciAcceptanceVerifier) wait(ctx context.Context, run func(context.Context) (ci.AcceptanceResult, error)) (jobs.AcceptanceVerdict, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	deadline := v.clock.Now().Add(30 * time.Minute)
	tick := v.ticker()
	defer tick.Stop()
	done := make(chan ciAcceptanceOutcome, 1)
	go func() { res, err := run(ctx); done <- ciAcceptanceOutcome{res, err} }()
	for {
		if !v.clock.Now().Before(deadline) {
			return jobs.AcceptanceVerdict{Reason: "acceptance timed out"}, nil
		}
		select {
		case <-ctx.Done():
			return jobs.AcceptanceVerdict{}, ctx.Err()
		case <-tick.C():
		case <-v.terminal:
			// A single terminal is only a wakeup; every kind must finish.
		case out := <-done:
			if !v.clock.Now().Before(deadline) {
				return jobs.AcceptanceVerdict{Reason: "acceptance timed out"}, nil
			}
			return jobs.AcceptanceVerdict{Passed: out.err == nil && out.result.Passed,
				CheckpointID: out.result.CheckpointID, TreeHash: out.result.TreeHash}, out.err
		}
	}
}
