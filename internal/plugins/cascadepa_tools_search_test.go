// Purpose: the cascade-pa tools wiring's SEARCH path — the chat.search leg
//
//	that makes S-44.T3's FTS5 index reachable from a harness, and the two
//	failure answers a caller must be able to tell apart: a store with no
//	index (fall back to the scan) and a daemon that is not there (report
//	it). Split from cascadepa_tools_wiring_test.go to stay under
//	Art.10.3's 300-line file cap; it shares that file's methodRPCDoer and
//	toolsAdapter helpers.
//
// SPORT: internal/plugins:cascadepa-tools-wiring (TEST) — P1-E20-W5-S44-T3.
package plugins

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/cascade-pa/tools"
)

func TestToolsWiringSearchMapsTheIndexResults(t *testing.T) {
	doer := &methodRPCDoer{searchRes: searchResultSetWire{Results: []searchResultWire{
		{ThreadID: "t1", TurnID: "u1", Content: "the migration ledger", Score: 4.5},
	}}}
	got, err := toolsAdapter(doer).Search(context.Background(), tools.SearchRequest{Query: "migration", Limit: 3})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("results = %d, want 1", len(got))
	}
	if got[0].TurnID != "u1" || got[0].Content != "the migration ledger" || got[0].Score != 4.5 {
		t.Errorf("result = %+v, want the wire row carried through unchanged", got[0])
	}
}

// TestToolsWiringSearchRecognisesAStoreWithNoIndex is the one that keeps the
// fallback honest.
//
// conversation.ErrSearchUnavailable crosses the wire as TEXT: the taxonomy
// kind survives, the identity does not, and comparing kinds is useless
// because KindUnsupported is shared with everything else the daemon refuses.
// So the adapter matches the sentinel's own message — and this asserts that
// the message it matches on is still the message the sentinel carries.
func TestToolsWiringSearchRecognisesAStoreWithNoIndex(t *testing.T) {
	if !strings.Contains(conversation.ErrSearchUnavailable.Error(), searchUnavailableMarker) {
		t.Fatalf("the marker %q is no longer part of %q; the fallback is dead",
			searchUnavailableMarker, conversation.ErrSearchUnavailable.Error())
	}
	doer := &methodRPCDoer{searchErr: conversation.ErrSearchUnavailable}
	_, err := toolsAdapter(doer).Search(context.Background(), tools.SearchRequest{Query: "x"})
	if err != tools.ErrSearchUnavailable {
		t.Fatalf("Search err = %v, want exactly tools.ErrSearchUnavailable so the scan fallback engages", err)
	}
}

func TestToolsWiringSearchPropagatesEveryOtherFailure(t *testing.T) {
	// A daemon that is not running must not look like a store with no
	// index: the first is reported, the second falls back to a scan that
	// cannot work either without a daemon.
	doer := &methodRPCDoer{searchErr: cascade.New(cascade.KindUnavailable, "daemon not running")}
	_, err := toolsAdapter(doer).Search(context.Background(), tools.SearchRequest{Query: "x"})
	if err == tools.ErrSearchUnavailable {
		t.Fatal("a transport failure was mistaken for a missing index")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Search err = %v, want KindUnavailable preserved", err)
	}
}

// TestToolsWiringSearchCannotBeTrickedIntoFallingBack closes the hole CR-A
// found in the first version of the fallback.
//
// The daemon reports a store with no index as text, so the adapter matched a
// substring of that message. But a QUERY containing the marker comes back
// inside SQLite's own MATCH syntax error, and a substring test alone would
// read that as "no index" and silently answer with a scan — turning a
// malformed query into a quiet change of backend. The syntax error is
// KindInvalidInput and the sentinel is KindUnsupported, so the kind is what
// tells them apart; the marker alone is forgeable by anyone who can type.
func TestToolsWiringSearchCannotBeTrickedIntoFallingBack(t *testing.T) {
	forged := cascade.Newf(cascade.KindInvalidInput,
		"conversation: search query rejected: fts5: syntax error near %q", searchUnavailableMarker)
	doer := &methodRPCDoer{searchErr: forged}
	_, err := toolsAdapter(doer).Search(context.Background(), tools.SearchRequest{Query: searchUnavailableMarker})
	if err == tools.ErrSearchUnavailable {
		t.Fatal("a rejected query quoting the marker was mistaken for a store with no index")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want the query rejection reported as KindInvalidInput", err)
	}
}
