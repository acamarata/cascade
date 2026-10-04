package resume

// Purpose: P1-CORE-19 acceptance for the claim table (claims.go) and the
//   Sweep scope rule: foreign entities are never written, and a failed
//   delete never leaves an id claimed.
// SPORT: internal.fleet.resume.Claims/ADDED (P1-CORE-19).

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestClaimsTryClaimRelease(t *testing.T) {
	c := NewClaims()
	if !c.TryClaim("a") || c.TryClaim("a") || !c.TryClaim("b") {
		t.Fatal("TryClaim: want a claimed once, b claimable")
	}
	c.Release("a")
	if !c.TryClaim("a") {
		t.Fatal("Release did not free a")
	}
}

// failDeleteStore fails every Delete: the sweep's DeleteTask fails.
type failDeleteStore struct{ provider.Store }

func (failDeleteStore) Delete(context.Context, string, string) error {
	return cascade.New(cascade.KindUnavailable, "injected: delete failed")
}

func TestSweepDeleteFailureReleasesClaim(t *testing.T) {
	f, ctx := newFanFixture(t), context.Background()
	f.seed(t, "stuck", 1, true)
	deps := f.deps
	deps.Store = failDeleteStore{Store: f.raw}
	if _, err := Sweep(ctx, deps, testInstant.Add(sweepTTL+time.Second), sweepTTL); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Sweep = %v, want the injected delete failure", err)
	}
	if !deps.Claims.TryClaim("stuck") {
		t.Fatal("after a failed delete the id is still claimed")
	}
}

func TestSweepIgnoresForeignEntities(t *testing.T) {
	f, ctx := newFanFixture(t), context.Background()
	payload, _ := json.Marshal(intentPayload{ActionID: "a1", TaskID: "dag-job"})
	for _, entity := range []string{"dag-job-1", "ci-run-1"} {
		if _, err := f.js.Append(ctx, entity, journal.KindIntent, "op-"+entity, payload); err != nil {
			t.Fatal(err)
		}
	}
	// fanout:Z whose seq 1 is not our cursor (a fence marker).
	fence, _ := json.Marshal(fenceMarker{T: "fence", ActionID: "x"})
	if _, err := f.js.Append(ctx, FanOutEntity("Z"), journal.KindResumeCursor, "fence", fence); err != nil {
		t.Fatal(err)
	}
	if err := f.raw.Put(ctx, requestsNamespace, "Z", []byte(`{"version":1,"fanout_id":"Z"}`)); err != nil {
		t.Fatal(err)
	}
	f.seed(t, "R", 1, true) // resumable, younger than ttl below
	heads := map[string]uint64{}
	for _, e := range []string{"dag-job-1", "ci-run-1", FanOutEntity("Z"), FanOutEntity("R")} {
		heads[e] = f.head(t, e)
	}
	if _, err := Sweep(ctx, f.deps, testInstant.Add(30*time.Minute), sweepTTL); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	for e, h := range heads {
		if got := f.head(t, e); got != h {
			t.Fatalf("%s head %d -> %d: a foreign or young entity was written", e, h, got)
		}
	}
	if _, err := f.raw.Get(ctx, requestsNamespace, "Z"); err != nil {
		t.Fatalf("foreign fanout:Z request key touched: %v", err)
	}
	if f.records(t, "R") != 1 {
		t.Fatal("resumable fanout:R lost its request record")
	}
}
