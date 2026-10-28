//go:build !windows

// Purpose: conversationLegAdapter's own conversion proof
// (daemon_unix_recall_what.go) — the D1cycle fix moved the
// conversation.Store <-> retrieval-owned type conversion here from
// internal/retrieval (which must never import internal/conversation, see
// internal/retrieval/recallwhat_conv.go's header); this file proves the
// conversion itself, in isolation from the real sqlite-backed store,
// against a fake satisfying conversationLegSource.
//
// Inputs: a fakeConversationLegSource returning canned
// conversation.TurnMatch/Segment/SensitivityTier values (and, for the
// error-path tests, canned errors). Outputs: the adapter's returned
// retrieval-owned values (or propagated error) compared field-by-field
// against what the source returned.
//
// Constraints: no sql.DB, no ApplyConversationSchema — this proves the
// adapter's own field mapping, not conversation.Store's persistence
// (that is internal/conversation's own test surface).
//
// SPORT: cmd/cascade/daemon (CHANGED — ci-fix11, conversationLegAdapter).
package main

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/pkg/provider"
)

// fakeConversationLegSource satisfies conversationLegSource with canned
// responses and captures the last SearchTurns filter it was called with.
type fakeConversationLegSource struct {
	matches    []conversation.TurnMatch
	searchErr  error
	segs       []conversation.Segment
	segsErr    error
	tier       provider.SensitivityTier
	tierErr    error
	lastFilter conversation.SearchFilter
}

func (f *fakeConversationLegSource) SearchTurns(_ context.Context, _ string, filter conversation.SearchFilter) ([]conversation.TurnMatch, error) {
	f.lastFilter = filter
	return f.matches, f.searchErr
}

func (f *fakeConversationLegSource) ListSegments(context.Context, string) ([]conversation.Segment, error) {
	return f.segs, f.segsErr
}

func (f *fakeConversationLegSource) ThreadPrivacy(context.Context, string) (provider.SensitivityTier, error) {
	return f.tier, f.tierErr
}

// TestConversationLegAdapter_SearchTurns_ConvertsAndPassesFilter proves
// both directions: the retrieval-owned RecallWhatSearchFilter reaches the
// source unchanged, and the source's conversation.TurnMatch rows come
// back as the equivalent retrieval.RecallWhatTurnMatch (ID/ThreadID/Role
// stringified/Rank).
func TestConversationLegAdapter_SearchTurns_ConvertsAndPassesFilter(t *testing.T) {
	src := &fakeConversationLegSource{
		matches: []conversation.TurnMatch{
			{Turn: conversation.Turn{ID: "t1", ThreadID: "th1", Role: conversation.RoleAssistant}, Rank: 0.5},
		},
	}
	a := conversationLegAdapter{store: src}
	got, err := a.SearchTurns(context.Background(), "hello", retrieval.RecallWhatSearchFilter{ThreadID: "th1", Limit: 7})
	if err != nil {
		t.Fatalf("SearchTurns: %v", err)
	}
	if src.lastFilter != (conversation.SearchFilter{ThreadID: "th1", Limit: 7}) {
		t.Fatalf("filter not passed through: got %+v", src.lastFilter)
	}
	want := []retrieval.RecallWhatTurnMatch{
		{Turn: retrieval.RecallWhatTurn{ID: "t1", ThreadID: "th1", Role: "assistant"}, Rank: 0.5},
	}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("SearchTurns conversion = %+v, want %+v", got, want)
	}
}

// TestConversationLegAdapter_SearchTurns_PropagatesError: the source's
// error must reach the caller unchanged, not be swallowed into an empty
// slice (the adapter must not turn a real failure into "no results").
func TestConversationLegAdapter_SearchTurns_PropagatesError(t *testing.T) {
	wantErr := errors.New("search unavailable")
	a := conversationLegAdapter{store: &fakeConversationLegSource{searchErr: wantErr}}
	_, err := a.SearchTurns(context.Background(), "hello", retrieval.RecallWhatSearchFilter{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("SearchTurns error = %v, want %v", err, wantErr)
	}
}

// TestConversationLegAdapter_ListSegments_ConvertsContent proves the
// Segment -> RecallWhatSegment mapping keeps Content and drops nothing
// the leg does not need.
func TestConversationLegAdapter_ListSegments_ConvertsContent(t *testing.T) {
	src := &fakeConversationLegSource{segs: []conversation.Segment{{Content: "hello world"}, {Content: "second"}}}
	a := conversationLegAdapter{store: src}
	got, err := a.ListSegments(context.Background(), "t1")
	if err != nil {
		t.Fatalf("ListSegments: %v", err)
	}
	want := []retrieval.RecallWhatSegment{{Content: "hello world"}, {Content: "second"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ListSegments conversion = %+v, want %+v", got, want)
	}
}

// TestConversationLegAdapter_ThreadPrivacy_PassesThrough proves the tier
// and any error reach the caller unchanged — this method needs no type
// conversion (provider.SensitivityTier is not an internal/conversation
// type), but the pass-through itself is the contract under test.
func TestConversationLegAdapter_ThreadPrivacy_PassesThrough(t *testing.T) {
	wantErr := errors.New("no such thread")
	a := conversationLegAdapter{store: &fakeConversationLegSource{tier: provider.SensitivityPublic, tierErr: wantErr}}
	tier, err := a.ThreadPrivacy(context.Background(), "th1")
	if tier != provider.SensitivityPublic {
		t.Fatalf("ThreadPrivacy tier = %v, want %v", tier, provider.SensitivityPublic)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("ThreadPrivacy error = %v, want %v", err, wantErr)
	}
}

// TestConversationLegAdapterForwardsScope: the adapter forwards the leg's
// project narrowing unchanged, adds the caller principal's own Scope as the
// attribution narrowing (overriding any attribution the filter carried, so a
// principal can never widen or swap it), leaves the owner's search
// unattributed, maps the untrusted marker, and internal/retrieval (every
// file, tests included) imports neither internal/conversation nor
// internal/principal.
func TestConversationLegAdapterForwardsScope(t *testing.T) {
	src := &fakeConversationLegSource{matches: []conversation.TurnMatch{
		{Turn: conversation.Turn{ID: "t1", ThreadID: "th1", Provenance: provider.ProvenanceUntrustedSource}},
		{Turn: conversation.Turn{ID: "t2", ThreadID: "th1", Provenance: provider.ProvenanceTrusted}},
		{Turn: conversation.Turn{ID: "t3", ThreadID: "th1"}},
	}}
	a := conversationLegAdapter{store: src}
	in := retrieval.RecallWhatSearchFilter{ProjectRef: "proj1", ProjectSet: true, Limit: 5, AttributedScope: "project:other"}
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	got, err := a.SearchTurns(owner, "q", in)
	if err != nil {
		t.Fatalf("owner SearchTurns: %v", err)
	}
	if src.lastFilter != (conversation.SearchFilter{ProjectRef: "proj1", ProjectSet: true, Limit: 5, AttributedScope: "project:other"}) {
		t.Fatalf("owner filter = %+v, want the leg's filter forwarded unchanged", src.lastFilter)
	}
	if !got[0].Turn.Untrusted || got[1].Turn.Untrusted || got[2].Turn.Untrusted {
		t.Fatalf("untrusted = %v %v %v, want true false false", got[0].Turn.Untrusted, got[1].Turn.Untrusted, got[2].Turn.Untrusted)
	}
	p := principal.Principal{ID: "p1", Kind: principal.KindSession, Scope: "project:mine"}
	asPrincipal := principal.WithCaller(context.Background(), principal.Caller{Principal: &p})
	if _, err := a.SearchTurns(asPrincipal, "q", in); err != nil {
		t.Fatalf("principal SearchTurns: %v", err)
	}
	want := conversation.SearchFilter{ProjectRef: "proj1", ProjectSet: true, Limit: 5, AttributedScope: "project:mine", AttributedSet: true}
	if src.lastFilter != want {
		t.Fatalf("principal filter = %+v, want %+v", src.lastFilter, want)
	}
	src.lastFilter = conversation.SearchFilter{}
	for _, scopeless := range []string{"", "project:", "personal:x"} {
		bad := principal.Principal{ID: "p2", Kind: principal.KindSession, Scope: scopeless}
		_, err := a.SearchTurns(principal.WithCaller(context.Background(), principal.Caller{Principal: &bad}), "q", in)
		if err != errNotInPrincipalScope || src.lastFilter != (conversation.SearchFilter{}) {
			t.Fatalf("scope %q: err = %v, filter = %+v; want the refusal before any search", scopeless, err, src.lastFilter)
		}
	}
	for _, imp := range retrievalImports(t) {
		if imp == "github.com/acamarata/cascade/internal/conversation" || imp == "github.com/acamarata/cascade/internal/principal" {
			t.Fatalf("internal/retrieval imports %s", imp)
		}
	}
}

// retrievalImports parses every .go file under internal/retrieval.
func retrievalImports(t *testing.T) []string {
	t.Helper()
	var out []string
	files := 0
	root := filepath.Join("..", "..", "internal", "retrieval")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		files++
		for _, spec := range f.Imports {
			imp, _ := strconv.Unquote(spec.Path.Value)
			out = append(out, imp)
		}
		return nil
	})
	if err != nil || files < 10 {
		t.Fatalf("walk internal/retrieval: %d files, %v", files, err)
	}
	return out
}
