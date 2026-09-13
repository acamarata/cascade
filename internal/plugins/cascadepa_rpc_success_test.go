// Purpose: success-path coverage for every cascadepa_*_wiring.go adapter
//
//	method (OneShot, Show, Edit, List, Act, Forget, CheckDigest), using
//	the rpcDoer seam (cascadepa_wiring.go) instead of a real socket:
//	internal/plugins is not on the egress ruling's allowed-"net"-importer
//	list, and internal/build's Art.7.2 gate bars "net"/"net/http" from
//	every untagged _test.go file, so no unit-lane test can drive these
//	success branches through an actual daemon connection. fakeRPCDoer is
//	a real collaborator satisfying the exact interface *client.Client
//	implements in production -- not a mock of the adapters under test.
//
// SPORT: internal/plugins:cascadepa-wiring (TEST) -- closes the ratchet
//
//	gap the three composition-root files' success branches otherwise
//	leave uncovered.
package plugins

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/memory/review"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
	pacmd "github.com/acamarata/cascade/plugins/cascade-pa/cmd"
)

// fakeRPCDoer stands in for *internal/client.Client's Do method. fill, if
// set, writes into out (a pointer to the same result type the real RPC
// method decodes into); err, if set, is returned instead.
type fakeRPCDoer struct {
	err          error
	fill         func(out any)
	calledMethod string
}

func (f *fakeRPCDoer) Do(_ context.Context, method string, _, out any) error {
	f.calledMethod = method
	if f.err != nil {
		return f.err
	}
	if f.fill != nil {
		f.fill(out)
	}
	return nil
}

func TestCascadePAClient_OneShot_SuccessMapsThreadAndTurn(t *testing.T) {
	doer := &fakeRPCDoer{fill: func(out any) {
		res := out.(*appendTurnResult)
		res.ThreadID = "thread-1"
		res.TurnID = "turn-2"
	}}
	c := newCascadePAClient(nil, 0, nil)
	c.doer = doer

	_, err := c.OneShot(context.Background(), pacmd.OneShotRequest{Prompt: "hi"})
	if err == nil {
		t.Fatal("OneShot: err = nil, want errReplyGenerationUnavailable")
	}
	if got := err.Error(); !containsAll(got, "thread thread-1 turn turn-2") {
		t.Errorf("OneShot: err = %q, want it to name the recorded thread/turn", got)
	}
	if doer.calledMethod == "" {
		t.Error("OneShot never called Do")
	}
}

func TestCascadePASoulClient_Show_Success(t *testing.T) {
	doer := &fakeRPCDoer{fill: func(out any) {
		res := out.(*memory.SoulShowResult)
		*res = memory.SoulShowResult{Body: "the body", Schema: "v1", Version: 3, Diverged: true}
	}}
	c := newCascadePASoulClient(nil, 0, nil)
	c.doer = doer

	view, err := c.Show(context.Background())
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	want := cascadepa.SoulChatView{Body: "the body", Schema: "v1", Version: 3, Diverged: true}
	if view != want {
		t.Errorf("Show: view = %+v, want %+v", view, want)
	}
}

func TestCascadePASoulClient_Edit_Success(t *testing.T) {
	doer := &fakeRPCDoer{fill: func(out any) {
		res := out.(*memory.SoulEditResult)
		res.Version = 4
	}}
	c := newCascadePASoulClient(nil, 0, nil)
	c.doer = doer

	res, err := c.Edit(context.Background(), cascadepa.SoulChatDocument{Body: "new body", Schema: "v1"})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if res.Version != 4 {
		t.Errorf("Edit: res.Version = %d, want 4", res.Version)
	}
}

func TestCascadePAReviewClient_List_Success(t *testing.T) {
	doer := &fakeRPCDoer{fill: func(out any) {
		res := out.(*review.ListResult)
		res.Pending = []memory.CandidateSummary{{ID: "c1", Kind: memory.KindUser, Sessions: 2, RefCount: 5}}
	}}
	c := newCascadePAReviewClient(nil, 0, nil)
	c.doer = doer

	listing, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listing.Pending) != 1 || listing.Pending[0].ID != "c1" || listing.Pending[0].RefCount != 5 {
		t.Errorf("List: listing = %+v, want one candidate c1 with RefCount 5", listing)
	}
}

func TestCascadePAReviewClient_Act_Success(t *testing.T) {
	doer := &fakeRPCDoer{fill: func(out any) {
		res := out.(*review.ActResult)
		res.Action = "accept"
		res.Changed = true
	}}
	c := newCascadePAReviewClient(nil, 0, nil)
	c.doer = doer

	res, err := c.Act(context.Background(), "c1", "accept")
	if err != nil {
		t.Fatalf("Act: %v", err)
	}
	if res.Action != "accept" || !res.Changed {
		t.Errorf("Act: res = %+v, want Action=accept Changed=true", res)
	}
}

func TestCascadePAReviewClient_Forget_Success(t *testing.T) {
	doer := &fakeRPCDoer{fill: func(out any) {
		res := out.(*memory.ForgetResult)
		res.Forgotten = true
	}}
	c := newCascadePAReviewClient(nil, 0, nil)
	c.doer = doer

	res, err := c.Forget(context.Background(), "c1")
	if err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if !res.Forgotten {
		t.Error("Forget: res.Forgotten = false, want true")
	}
}

// TestCascadePAReviewClient_CheckDigest_Success proves CheckDigest's real
// arithmetic (len(listing.Pending)) over a real List success, not just
// its error-propagation branch (already covered separately).
func TestCascadePAReviewClient_CheckDigest_Success(t *testing.T) {
	doer := &fakeRPCDoer{fill: func(out any) {
		res := out.(*review.ListResult)
		res.Pending = []memory.CandidateSummary{{ID: "c1"}, {ID: "c2"}, {ID: "c3"}}
	}}
	c := newCascadePAReviewClient(nil, 0, nil)
	c.doer = doer

	signal, err := c.CheckDigest(context.Background())
	if err != nil {
		t.Fatalf("CheckDigest: %v", err)
	}
	if signal.PendingCount != 3 {
		t.Errorf("CheckDigest: PendingCount = %d, want 3", signal.PendingCount)
	}
}
