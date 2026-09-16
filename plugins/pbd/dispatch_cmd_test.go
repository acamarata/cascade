package pbd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose (this file): the `pbd dispatch` command surface — where the
//   ticket comes from, and how a JSON-RPC response envelope is unpacked.
//
// Why it matters that these are tested in-package: the socket transport's
//   own round trip lives in the `integration`-tagged lane (it imports
//   net/http, which Art.7.2's unit lane forbids), but the branches that
//   decide WHAT gets dispatched and whether a reply is a success are pure
//   and belong here, where they run on every push.
//
// Constraints: no network. decodeRPCResult takes a reader, not a socket,
//   which is why it can be driven directly.
// SPORT: plugins/pbd tests (ADD) — P1-E14-W3-S30-T4.

const dispatchTestTicketID = "P1-E14-W3-S28-T1"

// TestFindTicketReadsTheTicketFile covers the source-of-truth rule: the
// ticket's model_class and tasks come off disk, never off the command line.
func TestFindTicketReadsTheTicketFile(t *testing.T) {
	root := t.TempDir()
	writeCleanTicket(t, root, dispatchTestTicketID)

	got, err := findTicket(root, dispatchTestTicketID)
	if err != nil {
		t.Fatalf("findTicket: %v", err)
	}
	if got.ModelClass != "build" {
		t.Errorf("model_class = %q, want the FILE's value", got.ModelClass)
	}
	if len(got.Tasks) != 3 {
		t.Errorf("tasks = %v, want the file's three", got.Tasks)
	}
}

// TestFindTicketRefusesWhatIsNotThere covers both ways the lookup fails.
func TestFindTicketRefusesWhatIsNotThere(t *testing.T) {
	root := t.TempDir()
	writeCleanTicket(t, root, dispatchTestTicketID)

	if _, err := findTicket(root, "P1-E99-W9-S99-T9"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("unknown ticket: err = %v, want KindNotFound", err)
	}
	if _, err := findTicket(t.TempDir(), dispatchTestTicketID); err == nil {
		t.Error("a tree with no such ticket produced a ticket")
	}
}

// TestDispatchCommandRefusesBeforeDialing proves the command validates its
// arguments and resolves the ticket BEFORE it opens a socket. A usage error
// that first dialled would report an unavailable daemon instead.
func TestDispatchCommandRefusesBeforeDialing(t *testing.T) {
	if err := runDispatchCommand(context.Background(), []string{"root", "id"}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("two args: err = %v, want KindInvalidInput (usage)", err)
	}
	// A socket path that could never be dialled: reaching the dial would
	// surface as KindUnavailable, so KindNotFound proves it stopped at the
	// ticket lookup.
	err := runDispatchCommand(context.Background(), []string{t.TempDir(), "P1-E99-W9-S99-T9", "/nonexistent/socket"})
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("unknown ticket: err = %v, want KindNotFound before any dial", err)
	}
}

// TestDecodeRPCResultUnpacksOneEnvelope covers every branch of the reply
// reader, including the one that matters most: a server-reported error must
// become a typed failure, never an empty success.
func TestDecodeRPCResultUnpacksOneEnvelope(t *testing.T) {
	t.Run("a server error is a typed failure, not an empty success", func(t *testing.T) {
		var resp provider.ModelResponse
		err := decodeRPCResult(strings.NewReader(
			`{"error":{"code":-32000,"message":"no lane available"}}`), "conductor.execute", &resp)
		if !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("err = %v, want KindUnavailable", err)
		}
		if !strings.Contains(err.Error(), "no lane available") {
			t.Errorf("err = %v, want the server's own message carried through", err)
		}
	})
	t.Run("a result decodes into out", func(t *testing.T) {
		var resp provider.ModelResponse
		if err := decodeRPCResult(strings.NewReader(
			`{"result":{"job_id":"job-7","output":"ran"}}`), "conductor.execute", &resp); err != nil {
			t.Fatalf("decodeRPCResult: %v", err)
		}
		if resp.JobID != "job-7" || resp.Output != "ran" {
			t.Errorf("resp = %+v", resp)
		}
	})
	t.Run("an empty result with no out is a success", func(t *testing.T) {
		if err := decodeRPCResult(strings.NewReader(`{"result":null}`), "job.cancel", nil); err != nil {
			t.Errorf("decodeRPCResult: %v", err)
		}
	})
	t.Run("a malformed envelope is an integrity failure", func(t *testing.T) {
		if err := decodeRPCResult(strings.NewReader(`not json`), "conductor.execute", nil); !cascade.HasKind(err, cascade.KindIntegrity) {
			t.Errorf("err = %v, want KindIntegrity", err)
		}
	})
	t.Run("a result of the wrong shape is an integrity failure", func(t *testing.T) {
		var resp provider.ModelResponse
		if err := decodeRPCResult(strings.NewReader(`{"result":"a string"}`), "conductor.execute", &resp); !cascade.HasKind(err, cascade.KindIntegrity) {
			t.Errorf("err = %v, want KindIntegrity", err)
		}
	})
}

// TestTheAgenticIntentAnswersOnlyItsOwnName proves the fall-through: an
// unrelated intent is left for the caller's own unknown-intent refusal
// rather than being swallowed here.
func TestTheAgenticIntentAnswersOnlyItsOwnName(t *testing.T) {
	handled, err := agenticIntent(context.Background(), "pbd.something.else")
	if handled || err != nil {
		t.Fatalf("handled = %v, err = %v; want the intent left unanswered", handled, err)
	}
	handled, err = agenticIntent(context.Background(), agenticIntentName)
	if !handled {
		t.Fatal("the agentic intent was not answered by its own handler")
	}
	if !errors.Is(err, ErrAgenticDispatchUnavailable) {
		t.Errorf("err = %v, want ErrAgenticDispatchUnavailable", err)
	}
}
