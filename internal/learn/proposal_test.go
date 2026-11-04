package learn

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
)

func TestProposalEvidenceRefs(t *testing.T) {
	isolateHome(t)
	store, _, clock := newTestStore(t)
	refs := []EvidenceRef{
		{Kind: "telemetry_outcome", ID: "17", RepoID: "repo-a", LaneID: "lane-a"},
		{Kind: string(jobs.EvidenceBuild), ID: "ev-02", RepoID: "repo-a", LaneID: "lane-b"},
	}
	sub := validSubmission()
	sub.Evidence = refs
	cfg, err := store.Submit(context.Background(), sub)
	if err != nil {
		t.Fatal(err)
	}
	trigger := Trigger{
		Signature:   FailureSignature("repo-a", "generated-code", "build", "low", FamilyToolFailure, CategoryCrash),
		Occurrences: MinOccurrences, DistinctSources: MinDistinctSources,
		Beneficiary: LaneIdentity{"l1", "node-a"},
		WindowStart: clock.Now().Add(-Window), WindowEnd: clock.Now(), Kind: "ci",
	}
	proposal := Proposal{Config: cfg, Trigger: trigger}
	got, err := store.Get(context.Background(), proposal.Config.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(proposal.Config.Evidence, refs) || !reflect.DeepEqual(got.Evidence, refs) {
		t.Fatalf("evidence references lost: proposal=%+v stored=%+v", proposal.Config.Evidence, got.Evidence)
	}
	if proposal.Config.Status != StatusPending || proposal.Config.Version != 0 {
		t.Fatalf("proposal is not pending: %+v", proposal.Config)
	}
	if proposal.Trigger != trigger || proposal.Trigger.WindowStart.IsZero() {
		t.Fatal("structured trigger changed")
	}
	assertFailureSignatureTuple(t)
}

func assertFailureSignatureTuple(t *testing.T) {
	t.Helper()
	want := fmt.Sprintf("%x", sha256.Sum256([]byte(`["repo-a","generated-code","build","low","tool_failure","crash"]`)))
	base := [6]string{"repo-a", "generated-code", "build", "low", "tool_failure", "crash"}
	sig := func(v [6]string) string {
		return FailureSignature(v[0], v[1], v[2], v[3], Family(v[4]), Category(v[5]))
	}
	if got := sig(base); got != want || len(got) != 64 {
		t.Fatalf("signature = %q, want %q", got, want)
	}
	for i := range base {
		other := base
		other[i] += "-other"
		if sig(other) == sig(base) {
			t.Fatalf("tuple field %d not included", i)
		}
	}
	a, b := base, base
	a[0], a[1] = "a|b", "c"
	b[0], b[1] = "a", "b|c"
	if sig(a) == sig(b) {
		t.Fatal("ambiguous tuple boundaries")
	}
}
