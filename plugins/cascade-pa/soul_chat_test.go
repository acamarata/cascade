package cascadepa

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// stubSoulChatClient is a minimal, injectable SoulChatClient. Each field
// left nil takes a no-op default so tests need to wire only what a given
// path exercises.
type stubSoulChatClient struct {
	showView SoulChatView
	showErr  error
	editRes  SoulChatEditResult
	editErr  error
	gotEdit  SoulChatDocument
}

func (s *stubSoulChatClient) Show(context.Context) (SoulChatView, error) {
	return s.showView, s.showErr
}

func (s *stubSoulChatClient) Edit(_ context.Context, doc SoulChatDocument) (SoulChatEditResult, error) {
	s.gotEdit = doc
	return s.editRes, s.editErr
}

func withSoulChatClient(t *testing.T, c SoulChatClient) {
	t.Helper()
	SetSoulChatClient(c)
	t.Cleanup(func() { SetSoulChatClient(nil) })
}

// TestHandleSoulChatCommand_NotThisCommand proves an ordinary chat prompt
// is never intercepted.
func TestHandleSoulChatCommand_NotThisCommand(t *testing.T) {
	reply, matched, err := HandleSoulChatCommand(context.Background(), "hello there")
	if matched || err != nil || reply != "" {
		t.Fatalf("HandleSoulChatCommand(hello) = (%q, %v, %v), want (\"\", false, nil)", reply, matched, err)
	}
}

// TestHandleSoulChatCommand_Success proves the happy path: an edit lands
// through the real seam and the reply carries the new version.
func TestHandleSoulChatCommand_Success(t *testing.T) {
	withSoulChatClient(t, &stubSoulChatClient{
		showView: SoulChatView{Body: "old", Schema: "cascade.soul/v1"},
		editRes:  SoulChatEditResult{Version: 3},
	})
	reply, matched, err := HandleSoulChatCommand(context.Background(), "/soul edit body I am Ada")
	if err != nil || !matched {
		t.Fatalf("HandleSoulChatCommand: err=%v matched=%v", err, matched)
	}
	if !strings.Contains(reply, "version 3") {
		t.Fatalf("reply = %q, want it to name version 3", reply)
	}
}

// TestHandleSoulChatCommand_VersionConflict proves a divergence reported
// by Show surfaces as an inline doctor note, and the session continues
// (no error, no panic).
func TestHandleSoulChatCommand_VersionConflict(t *testing.T) {
	withSoulChatClient(t, &stubSoulChatClient{
		showView: SoulChatView{Body: "old", Diverged: true},
		editRes:  SoulChatEditResult{Version: 5},
	})
	reply, matched, err := HandleSoulChatCommand(context.Background(), "/soul edit schema mine/v2")
	if err != nil || !matched {
		t.Fatalf("HandleSoulChatCommand: err=%v matched=%v", err, matched)
	}
	if !strings.Contains(reply, "doctor note") {
		t.Fatalf("reply = %q, want a doctor note for a diverged document", reply)
	}
}

// TestHandleSoulChatCommand_UnknownField proves a field outside the
// {body, schema} allowlist is a typed refusal, not a silent no-op.
func TestHandleSoulChatCommand_UnknownField(t *testing.T) {
	withSoulChatClient(t, &stubSoulChatClient{})
	_, matched, err := HandleSoulChatCommand(context.Background(), "/soul edit nickname Ada")
	if !matched {
		t.Fatal("HandleSoulChatCommand: want matched=true for a recognized trigger")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput", err)
	}
}

// TestHandleSoulChatCommand_ParseFailure proves a malformed invocation
// (missing value) is a typed refusal distinct from "not this command".
func TestHandleSoulChatCommand_ParseFailure(t *testing.T) {
	withSoulChatClient(t, &stubSoulChatClient{})
	_, matched, err := HandleSoulChatCommand(context.Background(), "/soul edit body")
	if !matched {
		t.Fatal("HandleSoulChatCommand: want matched=true for a recognized trigger")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want KindInvalidInput", err)
	}
}

// TestHandleSoulChatCommand_NoProfileContext proves the unconfigured
// client (no composition root has wired a SoulChatClient) refuses with an
// actionable message rather than a hang or a panic.
func TestHandleSoulChatCommand_NoProfileContext(t *testing.T) {
	SetSoulChatClient(nil)
	_, matched, err := HandleSoulChatCommand(context.Background(), "/soul edit body Ada")
	if !matched {
		t.Fatal("HandleSoulChatCommand: want matched=true for a recognized trigger")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
}

// TestHandleSoulChatCommand_StoreRPCError proves a real transport/store
// failure from Edit propagates as a typed error, not a silent success.
func TestHandleSoulChatCommand_StoreRPCError(t *testing.T) {
	withSoulChatClient(t, &stubSoulChatClient{
		showView: SoulChatView{},
		editErr:  cascade.New(cascade.KindUnavailable, "soul store unreachable"),
	})
	_, matched, err := HandleSoulChatCommand(context.Background(), "/soul edit body Ada")
	if !matched {
		t.Fatal("HandleSoulChatCommand: want matched=true for a recognized trigger")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
}

// TestHandleSoulChatCommand_ShowNotFoundIsBlank proves a never-written
// SOUL (Show returns KindNotFound) is treated as a blank starting point,
// not an error — the first `/soul edit` is how a user writes their SOUL
// for the first time, matching soulCurrentBody's own CLI-side contract.
func TestHandleSoulChatCommand_ShowNotFoundIsBlank(t *testing.T) {
	stub := &stubSoulChatClient{
		showErr: cascade.New(cascade.KindNotFound, "no soul document"),
		editRes: SoulChatEditResult{Version: 1},
	}
	withSoulChatClient(t, stub)
	_, matched, err := HandleSoulChatCommand(context.Background(), "/soul edit body first entry")
	if err != nil || !matched {
		t.Fatalf("HandleSoulChatCommand: err=%v matched=%v", err, matched)
	}
	if stub.gotEdit.Body != "first entry" {
		t.Fatalf("Edit doc = %+v, want Body = %q", stub.gotEdit, "first entry")
	}
}

// FuzzSoulChatCommandParser drives parseSoulEditTrigger against arbitrary
// input. The absolute contract: no input panics, and every non-match or
// refusal is reported through the function's own (matched, err) contract
// rather than a crash — a malformed corpus entry is a correct outcome, not
// a finding.
func FuzzSoulChatCommandParser(f *testing.F) {
	seeds := []string{
		"/soul edit body I am Ada. I like precision.",
		"/soul edit",
		"/soul editorial nonsense",
		"hello there",
		"/soul edit body",
		"/soul edit   ",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		field, value, matched, err := parseSoulEditTrigger(raw)
		if !matched {
			if field != "" || value != "" || err != nil {
				t.Fatalf("parseSoulEditTrigger(%q): unmatched result carried data: field=%q value=%q err=%v",
					raw, field, value, err)
			}
			return
		}
		if err != nil {
			return
		}
		if field == "" || value == "" {
			t.Fatalf("parseSoulEditTrigger(%q): matched with no error but empty field/value", raw)
		}
	})
}

// TestParseSoulEditTrigger_MutationProof pins the exact grammar boundary:
// a trigger without the trailing space before the field must still
// tokenize field/value correctly; a line sharing only a prefix with the
// trigger must not match.
func TestParseSoulEditTrigger_MutationProof(t *testing.T) {
	if _, _, matched, _ := parseSoulEditTrigger("/soul editorial nonsense"); matched {
		t.Fatal("parseSoulEditTrigger: a near-miss prefix must not match")
	}
	field, value, matched, err := parseSoulEditTrigger("/soul edit body hello world")
	if !matched || err != nil {
		t.Fatalf("parseSoulEditTrigger: matched=%v err=%v", matched, err)
	}
	if field != "body" || value != "hello world" {
		t.Fatalf("parseSoulEditTrigger = (%q, %q), want (body, hello world)", field, value)
	}
}
