// Purpose: Client's required tests (Show/Replay happy path, taxonomy-
//
//	error passthrough, plain-error-to-KindInternal wrapping), split out
//	of rpc_test.go purely to stay under the repo-wide 300-line-per-file
//	cap (R-14.117) — same subject rpc_client.go documents, sessions/
//	rpc_test.go's identical Client-test shape.
//
// SPORT: internal.fleet.journal.Client/ADDED (P1-E13-W3-S27-T4).
package journal_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/pkg/cascade"
)

// recordingCaller is a real-shape RPCCaller double whose Do decodes into
// the actual wire shape via a JSON round trip, matching
// sessions/rpc_test.go's own convention.
type recordingCaller struct {
	called bool
	method string
	params any
}

func (r *recordingCaller) Do(_ context.Context, method string, params, out any) error {
	r.called = true
	r.method = method
	r.params = params
	data, _ := json.Marshal(struct {
		Entries []journal.Entry `json:"entries"`
	}{})
	return json.Unmarshal(data, out)
}

func TestJournalClient_ShowHappyPath(t *testing.T) {
	caller := &recordingCaller{}
	client := journal.NewClient(caller)
	if _, err := client.Show(context.Background(), "e1", nil, 0); err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !caller.called || caller.method != journal.MethodShow {
		t.Fatalf("Client.Show did not call %q (called=%v method=%q)", journal.MethodShow, caller.called, caller.method)
	}
}

// TestJournalClient_ShowWithLimit proves a positive limit is actually
// forwarded onto the wire request's "limit" field, not silently dropped
// (Show's limit <= 0 branch is already exercised by
// TestJournalClient_ShowHappyPath's limit-0 call).
func TestJournalClient_ShowWithLimit(t *testing.T) {
	caller := &recordingCaller{}
	client := journal.NewClient(caller)
	if _, err := client.Show(context.Background(), "e1", nil, 25); err != nil {
		t.Fatalf("Show: %v", err)
	}
	encoded, err := json.Marshal(caller.params)
	if err != nil {
		t.Fatalf("marshal recorded params: %v", err)
	}
	var decoded struct {
		Limit *int `json:"limit"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode recorded params: %v", err)
	}
	if decoded.Limit == nil || *decoded.Limit != 25 {
		t.Fatalf("Show(limit=25) sent limit=%v, want 25", decoded.Limit)
	}
}

func TestJournalClient_ReplayHappyPath(t *testing.T) {
	caller := &recordingCaller{}
	client := journal.NewClient(caller)
	if _, err := client.Replay(context.Background(), "e1", nil); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if !caller.called || caller.method != journal.MethodReplay {
		t.Fatalf("Client.Replay did not call %q (called=%v method=%q)", journal.MethodReplay, caller.called, caller.method)
	}
}

// failingCaller is an RPCCaller double returning a fixed error, used to
// prove Client propagates both a taxonomy error unchanged and a plain
// error wrapped as KindInternal — sessions/rpc_test.go's identical
// pattern.
type failingCaller struct{ err error }

func (f *failingCaller) Do(context.Context, string, any, any) error { return f.err }

func TestJournalClient_TaxonomyErrorPassesThrough(t *testing.T) {
	want := cascade.New(cascade.KindUnavailable, "boom")
	client := journal.NewClient(&failingCaller{err: want})
	_, err := client.Show(context.Background(), "e1", nil, 0)
	if !errors.Is(err, want) {
		t.Fatalf("Show error = %v, want it to carry KindUnavailable", err)
	}
}

func TestJournalClient_PlainErrorWrappedAsInternal(t *testing.T) {
	client := journal.NewClient(&failingCaller{err: errors.New("transport exploded")})
	_, err := client.Replay(context.Background(), "e1", nil)
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("Replay error = %v, want KindInternal", err)
	}
}
