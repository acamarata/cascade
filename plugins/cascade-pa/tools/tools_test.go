package tools

// Purpose (this file): the dispatcher's own contract — what happens before
//   any individual tool runs. The refusal when no service is injected, the
//   unknown-tool answer, the registered names, and the parameter decoder
//   every tool shares.
//
// THE FAKE IS SHARED. fakeConversations below is the one test double for
//   this package; each tool's own _test.go drives it. It records what it
//   was asked for, so a test can assert the REQUEST a tool built and not
//   only the answer it returned.
//
// SPORT: plugins/cascade-pa:mcp-tools (TEST) — P1-E20-W5-S43-T4.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeConversations is the package's test double for the injected service.
type fakeConversations struct {
	appendResult AppendResult
	appendErr    error
	gotAppend    []AppendRequest

	threads    []ThreadSummary
	threadsErr error

	details   map[string]ThreadDetail
	detailErr map[string]error
	gotThread []string
}

func (f *fakeConversations) Append(_ context.Context, req AppendRequest) (AppendResult, error) {
	f.gotAppend = append(f.gotAppend, req)
	return f.appendResult, f.appendErr
}

func (f *fakeConversations) Thread(_ context.Context, id string) (ThreadDetail, error) {
	f.gotThread = append(f.gotThread, id)
	if err, ok := f.detailErr[id]; ok {
		return ThreadDetail{}, err
	}
	d, ok := f.details[id]
	if !ok {
		return ThreadDetail{}, cascade.Newf(cascade.KindNotFound, "no thread %q", id)
	}
	return d, nil
}

func (f *fakeConversations) Threads(_ context.Context) ([]ThreadSummary, error) {
	return f.threads, f.threadsErr
}

// dispatchJSON runs one tool and decodes its result into out.
func dispatchJSON(t *testing.T, d *Dispatcher, tool string, in string, out any) {
	t.Helper()
	raw, err := d.Dispatch(context.Background(), tool, []byte(in))
	if err != nil {
		t.Fatalf("%s(%s): %v", tool, in, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("%s: decoding result %s: %v", tool, raw, err)
	}
}

// dispatchErr runs one tool expecting a refusal, and returns it.
func dispatchErr(t *testing.T, d *Dispatcher, tool string, in string) error {
	t.Helper()
	raw, err := d.Dispatch(context.Background(), tool, []byte(in))
	if err == nil {
		t.Fatalf("%s(%s): err = nil, want a refusal (result was %s)", tool, in, raw)
	}
	return err
}

func TestDispatcherRefusesWithNoService(t *testing.T) {
	// The distinction this asserts is the whole reason ErrNoService
	// exists: "this process cannot reach the store" must never be
	// answered as "you have no conversations". An agent told the second
	// when the first is true reports lost history to its operator.
	for _, d := range []*Dispatcher{nil, NewDispatcher(nil)} {
		for _, name := range Names() {
			_, err := d.Dispatch(context.Background(), name, []byte(`{}`))
			// NOT errors.Is(err, ErrNoService): (*cascade.Error).Is
			// compares KIND ONLY, so that holds for ANY KindUnavailable
			// error — including the transport failure this refusal must
			// be told apart from. Identity, then the words an operator
			// reads.
			if err != ErrNoService {
				t.Fatalf("%s with no service: err = %v, want exactly ErrNoService", name, err)
			}
			if !cascade.HasKind(err, cascade.KindUnavailable) {
				t.Fatalf("%s with no service: err = %v, want KindUnavailable", name, err)
			}
			if !strings.Contains(err.Error(), "no conversation service is wired") {
				t.Fatalf("%s: refusal does not say what is missing: %v", name, err)
			}
		}
	}
}

func TestDispatcherRejectsAnUnknownTool(t *testing.T) {
	d := NewDispatcher(&fakeConversations{})
	err := dispatchErr(t, d, "cascade_cpa_delete_everything", `{}`)
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("unknown tool: err = %v, want KindNotFound", err)
	}
}

func TestNamesAreTheThreeRegisteredTools(t *testing.T) {
	// Pinned literally, not derived from the constants: these strings are
	// the MCP wire names a harness calls (R-16.20's compact profile), and
	// renaming a constant must not be able to silently rename the tool a
	// client already knows.
	want := []string{"cascade_cpa_send", "cascade_cpa_history", "cascade_cpa_search"}
	got := Names()
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Names()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDecoderRejectsAnUnknownField(t *testing.T) {
	// A model that sent {"contnet": "..."} asked to record something.
	// Ignoring the typo records an empty turn and reports success; that
	// is the failure an agent cannot see.
	d := NewDispatcher(&fakeConversations{appendResult: AppendResult{ThreadID: "t1", TurnID: "u1"}})
	err := dispatchErr(t, d, ToolSend, `{"contnet":"hello"}`)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("unknown field: err = %v, want KindInvalidInput", err)
	}
	if len(d.svc.(*fakeConversations).gotAppend) != 0 {
		t.Fatal("a refused decode still reached the conversation service")
	}
}

func TestDecoderTreatsEmptyAndNullAsTheZeroValue(t *testing.T) {
	// MCP clients send an absent argument object in several shapes. Each
	// must reach the tool's own validation rather than failing in the
	// decoder, so the caller is told what the TOOL needs.
	d := NewDispatcher(&fakeConversations{})
	for _, in := range []string{``, `  `, `null`} {
		err := dispatchErr(t, d, ToolSend, in)
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("send(%q): err = %v, want the tool's own KindInvalidInput", in, err)
		}
	}
	// History has no required field, so the same empty input must SUCCEED
	// and fall through to the thread listing.
	var out HistoryOutput
	dispatchJSON(t, d, ToolHistory, ``, &out)
	if out.Items == nil {
		t.Fatal("history with empty input returned a nil items list; the field must always be present")
	}
}

func TestDecoderRejectsMalformedJSON(t *testing.T) {
	d := NewDispatcher(&fakeConversations{})
	for _, name := range Names() {
		err := dispatchErr(t, d, name, `{"query":`)
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("%s with truncated JSON: err = %v, want KindInvalidInput", name, err)
		}
	}
}
