//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

type evidenceReadFailure struct{ err error }

func (r evidenceReadFailure) QueryAll(context.Context, string) ([]jobs.EvidenceRecord, error) {
	return nil, r.err
}

func TestJobsEvidenceReadError(t *testing.T) {
	want := cascade.New(cascade.KindIntegrity, "injected ledger read failure")
	h := jobsEvidenceList(evidenceReadFailure{want})
	if _, err := h(context.Background(), json.RawMessage(`{"ticket":"A"}`)); err != want || err.Error() != want.Error() {
		t.Fatalf("read error = %v", err)
	}
	h = jobsEvidenceList(evidenceReadFailure{jobs.ErrNoEvidence})
	got, err := h(context.Background(), json.RawMessage(`{"ticket":"A"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got)
	if string(raw) != `{"ticket":"A","rows":[]}` {
		t.Fatalf("empty = %s", raw)
	}
	for _, raw := range []string{`{}`, `{"ticket":""}`, `{"ticket":"A","ceiling":"secret"}`, `{"ticket":1}`, `{"ticket":"A"} {}`} {
		if _, err := h(context.Background(), json.RawMessage(raw)); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("params %s: %v", raw, err)
		}
	}
}

func TestJobsEvidenceOwnerAdmission(t *testing.T) {
	r := rpc.NewRegistry()
	r.SetAccessPolicy(nil)
	for _, method := range []string{"jobs.evidence.list", "plugin.pbd.evidence"} {
		r.Register(method, jobsEvidenceList(evidenceReadFailure{jobs.ErrNoEvidence}))
		owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
		request := &rpc.Request{Method: method, Params: json.RawMessage(`{"ticket":"A"}`)}
		if _, err := r.Dispatch(owner, request); err != nil {
			t.Fatal(err)
		}
		for _, ctx := range []context.Context{context.Background(), principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{ID: "scoped"}})} {
			if _, err := r.Dispatch(ctx, request); err == nil || err.Code != cascade.KindPermissionDenied.JSONRPCCode() {
				t.Fatalf("%s admission: %+v", method, err)
			}
		}
	}
}

func TestJobsEvidenceWireView(t *testing.T) {
	for _, tier := range []provider.SensitivityTier{provider.SensitivityPublic, provider.SensitivityInternal, provider.SensitivityRestricted, provider.SensitivityLocalOnly} {
		row := evidenceView(jobs.EvidenceRecord{JobID: "A", DataClass: &tier, ArtifactRef: "artifact", ResultHash: "result", AttestorIdentity: "daemon:d", IdempotencyKey: "key"})
		raw, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		if row.Redacted != tier.MoreRestrictiveThan(jobs.EvidenceReadCeiling) {
			t.Fatalf("tier %v: %s", tier, raw)
		}
		for _, key := range []string{"artifact_ref", "result_hash", "attestor_identity", "idempotency_key"} {
			if strings.Contains(string(raw), `"`+key+`"`) == row.Redacted {
				t.Fatalf("content field %s: %s", key, raw)
			}
		}
	}
}
