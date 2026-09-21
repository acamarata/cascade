// Package topics (observe_pin_test.go): the pin that holds observe mode and
// apply mode to ONE routing decision. Observe reports what Route would do;
// if the two can ever disagree about the same window against the same store,
// the observe-log event is telling the operator about a pipeline that does
// not exist. So the assertion here is not "observe produced two plausible
// ids" - it is "observe's proposed ids are byte-identical to the ids Route
// itself then returned for the same input".
//
// This is the test the original draft lacked: it re-derived the thread id
// from a package constant instead of asking the store, and every existing
// assertion (len == 2) stayed green while the emitted ids named threads the
// package's own ThreadStore double never issues. Helpers come from
// observe_doubles_test.go.
package topics

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestObserveProposalsMatchRouteSelection is the pin for the state that
// matters most: the threads already exist, so observe mode's proposals are
// real ids and must equal, id for id, what Route selects for the same window
// against the same store.
func TestObserveProposalsMatchRouteSelection(t *testing.T) {
	clock, fts := newFixedClock(), newFakeThreadStore()
	at := mustAutoThreader(t, codeBoundarySegmenter(), fts)
	// Prime the store the way production would: one earlier Route.
	primed, err := at.Route(context.Background(), observeTurns())
	if err != nil {
		t.Fatalf("priming Route: %v", err)
	}
	ol := mustObserveLogger(t, at, storetest.NewMemStore(), clock, &fakePublisher{})
	observed, err := ol.Observe(context.Background(), observeTurns())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	wantEq(t, observed.Observed, true, "ObserveResult.Observed")
	gotIDs := proposalThreadIDs(observed.Proposals)
	if !slices.Equal(gotIDs, primed) {
		t.Fatalf("observe proposed %v, but Route selected %v for the same window and store", gotIDs, primed)
	}
	// And Route run AGAIN after the observation still agrees.
	again, err := at.Route(context.Background(), observeTurns())
	if err != nil {
		t.Fatalf("Route after Observe: %v", err)
	}
	if !slices.Equal(gotIDs, again) {
		t.Fatalf("observe proposed %v, but the next Route selected %v", gotIDs, again)
	}
	for _, p := range observed.Proposals {
		wantEq(t, p.Existing, true, "Proposal.Existing for a topic whose thread is already there")
	}
}

// TestObserveProposalMarkersNameWhatRouteCreates is the other half: with no
// thread for either topic yet, observe mode proposes would_create markers
// and creates nothing - and once Route has created the threads, a second
// observation names exactly the ids Route returned.
func TestObserveProposalMarkersNameWhatRouteCreates(t *testing.T) {
	clock, fts := newFixedClock(), newFakeThreadStore()
	at := mustAutoThreader(t, codeBoundarySegmenter(), fts)
	ol := mustObserveLogger(t, at, storetest.NewMemStore(), clock, &fakePublisher{})
	observed, err := ol.Observe(context.Background(), observeTurns())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	wantEq(t, fts.threadSeq, 0, "threads created by Observe (must be none)")
	wantMarkers := []ThreadID{"would_create:fallback", "would_create:code-topic"}
	if !slices.Equal(proposalThreadIDs(observed.Proposals), wantMarkers) {
		t.Fatalf("observe proposed %v, want the would_create markers %v",
			proposalThreadIDs(observed.Proposals), wantMarkers)
	}
	for _, p := range observed.Proposals {
		wantEq(t, p.Existing, false, "Proposal.Existing for a topic with no thread yet")
	}
	// Route now creates them, in the SAME planned order, and a second
	// observation names exactly those ids - the marker's promise kept.
	created, err := at.Route(context.Background(), observeTurns())
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	wantEq(t, len(created), len(observed.Proposals), "threads Route created vs proposals observed")
	second, err := ol.Observe(context.Background(), observeTurns())
	if err != nil {
		t.Fatalf("second Observe: %v", err)
	}
	if !slices.Equal(proposalThreadIDs(second.Proposals), created) {
		t.Fatalf("after Route, observe proposed %v, want Route's own ids %v",
			proposalThreadIDs(second.Proposals), created)
	}
}

// TestObserveLookupErrorMatchesApplyModeFailure: a ThreadStore that cannot
// answer fails BOTH modes. Observe mode must not report a routing success
// apply mode would not have had (CR finding 5).
func TestObserveLookupErrorMatchesApplyModeFailure(t *testing.T) {
	wantErr := errors.New("thread store down")
	clock := newFixedClock()
	fts := newFakeThreadStore()
	fts.lookupErr = wantErr
	fts.createErr = wantErr
	at := mustAutoThreader(t, codeBoundarySegmenter(), fts)
	pub := &fakePublisher{}
	ol := mustObserveLogger(t, at, storetest.NewMemStore(), clock, pub)

	_, observeErr := ol.Observe(context.Background(), observeTurns())
	if !errors.Is(observeErr, wantErr) {
		t.Fatalf("observe-mode error = %v, want the store's own %v", observeErr, wantErr)
	}
	wantEq(t, len(pub.calls), 0, "audit events published after a store failure")

	applyStore := storetest.NewMemStore()
	seedFirstUseAt(t, applyStore, clock.now.Add(-8*24*time.Hour))
	applyOL := mustObserveLogger(t, at, applyStore, clock, pub)
	_, applyErr := applyOL.Observe(context.Background(), observeTurns())
	if !errors.Is(applyErr, wantErr) {
		t.Fatalf("apply-mode error = %v, want the same store failure %v", applyErr, wantErr)
	}
}
