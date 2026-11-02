package ci

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestNewLocalAttestorRequiresEveryPart(t *testing.T) {
	r := newLocalAttestRig(t)
	if _, err := NewLocalAttestor(r.a.self, nil, r.store, r.a.trust, func() string { return "p" }, r.a.clock); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("missing signer accepted: %v", err)
	}
	r.a.BindSources(r.a.db, nil)
	if r.a.ledger != nil {
		t.Fatal("BindSources kept a ledger it was not given")
	}
}

func TestAcceptancePlanFallsBackToFull(t *testing.T) {
	plan := AcceptancePlan(context.Background(), JobRef{PlannedRisk: jobs.RiskClassHigh}, RequirementModel{}, CandidateSnapshot{TreeHash: "tree"})
	if plan.Selection != TargetSelectionFull || plan.CandidateTreeHash != "tree" {
		t.Fatalf("plan = %+v", plan)
	}
}

// TestAttestRerunRefusals walks the refusals before and during signing.
func TestAttestRerunRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(*localAttestRig)
		sql    string
	}{
		"unknown nonce":         {mutate: func(r *localAttestRig) { r.nonce = "not-issued" }},
		"other job":             {mutate: func(r *localAttestRig) { r.ref.JobID = "other" }},
		"other checkpoint":      {mutate: func(r *localAttestRig) { r.res.CheckpointID = "other" }},
		"no results":            {mutate: func(r *localAttestRig) { r.res.Results = nil }},
		"run row missing":       {mutate: func(r *localAttestRig) { r.res.Results[0].RunID = -92 }},
		"other commit":          {mutate: func(r *localAttestRig) { r.ref.CheckpointCommit = "other" }},
		"tree differs":          {mutate: func(r *localAttestRig) { r.res.Results[0].TreeHash = "other" }},
		"run not completed":     {sql: "UPDATE ci_run SET status = 'in_progress'"},
		"stream binding gone":   {sql: "UPDATE ci_run SET head_sha = ''"},
		"jobs not terminal":     {sql: "UPDATE ci_job SET status = 'in_progress'"},
		"jobs table unreadable": {sql: "DROP TABLE ci_job"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newLocalAttestRig(t)
			if tc.mutate != nil {
				tc.mutate(&r)
			}
			if tc.sql != "" {
				if _, err := r.a.db.Exec(tc.sql); err != nil {
					t.Fatal(err)
				}
			}
			if id, err := r.a.AttestRerun(context.Background(), r.res, r.ref, r.nonce, r.expires); err == nil {
				t.Fatalf("signed despite %s: %s", name, id)
			}
			if rows, err := r.store.ByCommitSHA(context.Background(), "commit"); err != nil || len(rows) != 0 {
				t.Fatalf("refusal stored a row: %v %v", rows, err)
			}
		})
	}
}

// TestAttestRerunRecordsAFailedRun signs a run that did not conclude in
// success with a non-zero exit status instead of refusing it.
func TestAttestRerunRecordsAFailedRun(t *testing.T) {
	for name, sql := range map[string]string{
		"run failed": "UPDATE ci_run SET conclusion = 'failure'",
		"job failed": "UPDATE ci_job SET conclusion = 'failure'",
	} {
		t.Run(name, func(t *testing.T) {
			r := newLocalAttestRig(t)
			if _, err := r.a.db.Exec(sql); err != nil {
				t.Fatal(err)
			}
			id, err := r.a.AttestRerun(context.Background(), r.res, r.ref, r.nonce, r.expires)
			if err != nil {
				t.Fatal(err)
			}
			row, ok, err := r.store.ByID(context.Background(), id)
			if err != nil || !ok || row.ExitStatus == 0 || row.AttestorClass != string(attest.ClassEnrolledNode) {
				t.Fatalf("row = %+v %v %v", row, ok, err)
			}
		})
	}
}
