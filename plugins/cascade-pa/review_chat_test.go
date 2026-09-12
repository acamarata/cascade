package cascadepa

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// stubReviewChatClient is a minimal, injectable ReviewChatClient.
type stubReviewChatClient struct {
	listing   ReviewChatListing
	listErr   error
	actRes    ReviewChatActResult
	actErr    error
	forgetRes ReviewChatForgetResult
	forgetErr error
	gotAction string
	gotID     string
}

func (s *stubReviewChatClient) List(context.Context) (ReviewChatListing, error) {
	return s.listing, s.listErr
}

func (s *stubReviewChatClient) Act(_ context.Context, id, action string) (ReviewChatActResult, error) {
	s.gotID, s.gotAction = id, action
	return s.actRes, s.actErr
}

func (s *stubReviewChatClient) Forget(_ context.Context, id string) (ReviewChatForgetResult, error) {
	s.gotID = id
	return s.forgetRes, s.forgetErr
}

func withReviewChatClient(t *testing.T, c ReviewChatClient) {
	t.Helper()
	SetReviewChatClient(c)
	t.Cleanup(func() { SetReviewChatClient(nil) })
}

func TestHandleReviewChatCommand_NotThisCommand(t *testing.T) {
	reply, matched, err := HandleReviewChatCommand(context.Background(), "hello there")
	if matched || err != nil || reply != "" {
		t.Fatalf("HandleReviewChatCommand(hello) = (%q, %v, %v), want not matched", reply, matched, err)
	}
}

// TestHandleReviewChatCommand_NextWithCandidate proves the candidate card
// is presented client-side from memory.review.list's pending section.
func TestHandleReviewChatCommand_NextWithCandidate(t *testing.T) {
	withReviewChatClient(t, &stubReviewChatClient{
		listing: ReviewChatListing{Pending: []ReviewChatCandidate{
			{ID: "note/a", Kind: "note", Sessions: 2, RefCount: 3},
		}},
	})
	reply, matched, err := HandleReviewChatCommand(context.Background(), "/memory review next")
	if err != nil || !matched {
		t.Fatalf("HandleReviewChatCommand: err=%v matched=%v", err, matched)
	}
	if !strings.Contains(reply, "note/a") || !strings.Contains(reply, "hits: 3") {
		t.Fatalf("reply = %q, want the candidate card", reply)
	}
}

// TestHandleReviewChatCommand_NextEmpty proves an empty queue is
// acknowledged, never treated as an error.
func TestHandleReviewChatCommand_NextEmpty(t *testing.T) {
	withReviewChatClient(t, &stubReviewChatClient{})
	reply, matched, err := HandleReviewChatCommand(context.Background(), "/memory review next")
	if err != nil || !matched {
		t.Fatalf("HandleReviewChatCommand: err=%v matched=%v", err, matched)
	}
	if reply != "nothing pending" {
		t.Fatalf("reply = %q, want %q", reply, "nothing pending")
	}
}

// TestHandleReviewChatCommand_Approve proves approve dispatches to Act
// with the matching action.
func TestHandleReviewChatCommand_Approve(t *testing.T) {
	stub := &stubReviewChatClient{actRes: ReviewChatActResult{Action: "approve", Changed: true}}
	withReviewChatClient(t, stub)
	reply, matched, err := HandleReviewChatCommand(context.Background(), "/memory review approve note/a")
	if err != nil || !matched {
		t.Fatalf("HandleReviewChatCommand: err=%v matched=%v", err, matched)
	}
	if stub.gotAction != "approve" || stub.gotID != "note/a" {
		t.Fatalf("Act called with (%q, %q), want (note/a, approve)", stub.gotID, stub.gotAction)
	}
	if !strings.Contains(reply, "applied") {
		t.Fatalf("reply = %q, want it to confirm the change", reply)
	}
}

// TestHandleReviewChatCommand_Skip proves skip dispatches to Act with the
// matching action.
func TestHandleReviewChatCommand_Skip(t *testing.T) {
	stub := &stubReviewChatClient{actRes: ReviewChatActResult{Action: "skip", Changed: false}}
	withReviewChatClient(t, stub)
	reply, matched, err := HandleReviewChatCommand(context.Background(), "/memory review skip note/a")
	if err != nil || !matched {
		t.Fatalf("HandleReviewChatCommand: err=%v matched=%v", err, matched)
	}
	if stub.gotAction != "skip" {
		t.Fatalf("Act action = %q, want skip", stub.gotAction)
	}
	if !strings.Contains(reply, "no change") {
		t.Fatalf("reply = %q, want it to report no change", reply)
	}
}

// TestHandleReviewChatCommand_Forget proves forget dispatches to the
// DISTINCT Forget method, never to Act.
func TestHandleReviewChatCommand_Forget(t *testing.T) {
	stub := &stubReviewChatClient{forgetRes: ReviewChatForgetResult{Forgotten: true}}
	withReviewChatClient(t, stub)
	reply, matched, err := HandleReviewChatCommand(context.Background(), "/memory review forget note/a")
	if err != nil || !matched {
		t.Fatalf("HandleReviewChatCommand: err=%v matched=%v", err, matched)
	}
	if stub.gotAction != "" {
		t.Fatalf("Act was called (action=%q); forget must dispatch to Forget only", stub.gotAction)
	}
	if !strings.Contains(reply, "forgotten") {
		t.Fatalf("reply = %q, want it to confirm the forget", reply)
	}
}

// TestHandleReviewChatCommand_UnknownCandidateID proves a typed error from
// the real API (KindNotFound) propagates rather than being swallowed.
func TestHandleReviewChatCommand_UnknownCandidateID(t *testing.T) {
	withReviewChatClient(t, &stubReviewChatClient{
		actErr: cascade.New(cascade.KindNotFound, "no such candidate"),
	})
	_, matched, err := HandleReviewChatCommand(context.Background(), "/memory review approve note/missing")
	if !matched {
		t.Fatal("HandleReviewChatCommand: want matched=true")
	}
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("err = %v, want KindNotFound", err)
	}
}

// TestHandleReviewChatCommand_RPCError proves a transport/store failure
// propagates as a typed error.
func TestHandleReviewChatCommand_RPCError(t *testing.T) {
	withReviewChatClient(t, &stubReviewChatClient{
		listErr: cascade.New(cascade.KindUnavailable, "daemon unreachable"),
	})
	_, matched, err := HandleReviewChatCommand(context.Background(), "/memory review next")
	if !matched {
		t.Fatal("HandleReviewChatCommand: want matched=true")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
}

// TestHandleReviewChatCommand_ParseFailure proves a malformed invocation
// (missing id) is a typed refusal.
func TestHandleReviewChatCommand_ParseFailure(t *testing.T) {
	withReviewChatClient(t, &stubReviewChatClient{})
	_, matched, err := HandleReviewChatCommand(context.Background(), "/memory review approve")
	if !matched {
		t.Fatal("HandleReviewChatCommand: want matched=true")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput", err)
	}
}

// TestHandleReviewChatCommand_NoProfileContext proves the unconfigured
// client refuses with an actionable message.
func TestHandleReviewChatCommand_NoProfileContext(t *testing.T) {
	SetReviewChatClient(nil)
	_, matched, err := HandleReviewChatCommand(context.Background(), "/memory review next")
	if !matched {
		t.Fatal("HandleReviewChatCommand: want matched=true")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
}

// FuzzReviewChatCommandParser drives parseReviewChatTrigger against
// arbitrary input. No input may panic; every non-match or refusal is
// reported through the function's own contract.
func FuzzReviewChatCommandParser(f *testing.F) {
	seeds := []string{
		"/memory review next",
		"/memory review approve note/a",
		"/memory review skip note/a",
		"/memory review forget note/a",
		"/memory review",
		"/memory reviewer nonsense",
		"hello there",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		verb, id, matched, err := parseReviewChatTrigger(raw)
		if !matched {
			if verb != "" || id != "" || err != nil {
				t.Fatalf("parseReviewChatTrigger(%q): unmatched result carried data: verb=%q id=%q err=%v",
					raw, verb, id, err)
			}
			return
		}
		if err != nil {
			return
		}
		switch verb {
		case reviewVerbNext:
			if id != "" {
				t.Fatalf("parseReviewChatTrigger(%q): next carried an id %q", raw, id)
			}
		case reviewVerbApprove, reviewVerbSkip, reviewVerbForget:
			if id == "" {
				t.Fatalf("parseReviewChatTrigger(%q): %s matched with no id", raw, verb)
			}
		default:
			t.Fatalf("parseReviewChatTrigger(%q): matched with unknown verb %q and no error", raw, verb)
		}
	})
}
