//go:build security || integration

package delegation

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestRedTeamEvidenceInsertionDenied exercises authorization and the actual append boundary.
func TestRedTeamEvidenceInsertionDenied(t *testing.T) {
	f := newCollectFixture(t)
	ctx := context.Background()
	leases, auth := evidenceAuthorization(t, f.chain)
	store := f.chain.st
	if err := jobs.NewProducerAuthz(store, func() bool { return true }, leases).Authorize(ctx, auth); err != nil {
		t.Fatal(err)
	}
	child, err := f.resolve()
	if err != nil {
		t.Fatal(err)
	}
	session := child
	session.ID, session.Kind = "delegated-session", principal.KindSession
	token, err := f.c.service.deps.Minter.Mint(session, f.c.env.ExpiresAt.Sub(t0))
	if err != nil {
		t.Fatal("mint delegated session:", err)
	}
	session, err = f.c.service.deps.Minter.Resolve(token, t0)
	if err != nil {
		t.Fatal("resolve delegated session:", err)
	}
	for _, p := range []principal.Principal{child, session, {ID: "node-daemon", Kind: principal.KindNode}, {ID: "unenrolled", Kind: principal.KindRemoteClient}} {
		t.Run(string(p.Kind), func(t *testing.T) {
			caller := principal.WithCaller(ctx, principal.Caller{Principal: &p})
			forged := auth
			forged.ExecutionID = "forged-execution"
			assertEvidenceDenied(caller, t, f.chain, jobs.NewProducerAuthz(store, func() bool { return false }, leases), forged)
			// Knowing a real execution id must not bypass process identity either.
			assertEvidenceDenied(caller, t, f.chain, jobs.NewProducerAuthz(store, func() bool { return false }, leases), auth)
		})
	}
	auth.LeaseEpoch--
	fenceErr := leases.Fence(ctx, auth.LeaseRepoID, auth.LeaseScopeGlob, auth.LeaseEpoch)
	assertSentinelChain(t, fenceErr, jobs.ErrLeaseFenced)
	assertEvidenceDenied(ctx, t, f.chain, jobs.NewProducerAuthz(store, func() bool { return true }, leases), auth)
}

func evidenceAuthorization(t *testing.T, c *chain) (*jobs.LeaseManager, jobs.AppendAuthorization) {
	t.Helper()
	ctx := context.Background()
	clock := runtime.NewFixedClock(t0)
	if err := jobs.ApplyJobsSchema(ctx, c.db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := jobs.ApplyEvidenceSchema(ctx, c.db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatal(err)
	}
	store := c.st
	class := provider.SensitivityInternal
	if err := store.PutJob(ctx, jobs.Job{ID: "evidence-job", State: jobs.JobStatePending, ConsequenceClass: jobs.ConsequenceNormal, DataClass: &class}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutExecution(ctx, jobs.Execution{ID: "controller-execution", JobID: "evidence-job", State: jobs.ExecutionRunning}); err != nil {
		t.Fatal(err)
	}
	leases := jobs.NewLeaseManager(store, clock, func() bool { return true }, jobs.DefaultLeaseDefaults(), nil)
	grant, err := leases.Acquire(ctx, "repo", "internal/**", "evidence-job")
	if err != nil {
		t.Fatal(err)
	}
	auth := jobs.AppendAuthorization{ExecutionID: "controller-execution", LeaseRepoID: "repo", LeaseScopeGlob: "internal/**", LeaseEpoch: grant.Lease.Epoch}
	return leases, auth
}

func assertSentinelChain(t *testing.T, err error, sentinel *cascade.Error) {
	t.Helper()
	wantCascadeErr(t, err, sentinel.Kind, sentinel.Msg)
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		if cause == sentinel {
			return
		}
	}
	t.Fatalf("error chain lacks the expected sentinel: %v", err)
}

func assertEvidenceDenied(ctx context.Context, t *testing.T, c *chain, authz *jobs.ProducerAuthz, auth jobs.AppendAuthorization) {
	t.Helper()
	assertSentinelChain(t, authz.Authorize(ctx, auth), jobs.ErrEvidenceProducerDenied)
	effects, err := audit.NewEffectLog(audit.New(storetest.NewMemStore(), runtime.NewFixedClock(t0), nil))
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := jobs.NewEvidenceLedger(c.st, runtime.NewFixedClock(t0), effects, authz)
	if err != nil {
		t.Fatal(err)
	}
	row, err := ledger.Append(ctx, jobs.EvidenceRecord{JobID: "evidence-job", Kind: jobs.EvidenceBuild,
		ProducerCapability: jobs.ProducerControllerRun, AttestorIdentity: "daemon:forged",
		Outcome: jobs.OutcomePass, IdempotencyKey: "forged-proof", ArtifactRef: "proof", ResultHash: "result"}, auth)
	assertSentinelChain(t, err, jobs.ErrEvidenceProducerDenied)
	if row.Seq != 0 {
		t.Fatal("denied append returned a row")
	}
	var count int
	if err := c.db.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM jobs_evidence) + (SELECT count(*) FROM jobs_evidence_class)").Scan(&count); err != nil || count != 0 {
		t.Fatalf("denied append left %d evidence rows: %v", count, err)
	}
}
