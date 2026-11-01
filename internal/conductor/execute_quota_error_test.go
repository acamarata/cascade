package conductor

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose (this file): a quota refusal from Router.Select keeps its kind and
//   its lane names through Execute and ExecuteStream, while every other
//   Select refusal stays the bare ErrNoLane.
// Constraints: identity is proven by walking the chain for the sentinel
//   pointer, because errors.Is on cascade errors compares Kind only.
// SPORT: conductor.execute tests (ADD, P1-CAP-07).

// quotaRefusalCase is one row of the contract table.
type quotaRefusalCase struct {
	name     string
	selErr   error
	sentinel *cascade.Error
	kind     cascade.Kind
	msg      string
}

// TestExecuteSelectQuotaErrorKeepsKind is the contract table.
func TestExecuteSelectQuotaErrorKeepsKind(t *testing.T) {
	cases := []quotaRefusalCase{
		{"all lanes exhausted", exhaustedWithCeilings([]LaneID{"laneA"}), ErrAllLanesExhausted, cascade.KindQuotaExhausted, "ceiling reached for: laneA"},
		{"lane ceiling reached", ErrLaneCeilingReached, ErrLaneCeilingReached, cascade.KindQuotaExhausted, "lane ceiling reached"},
		{"not a quota error", cascade.New(cascade.KindUnavailable, "registry down"), nil, cascade.KindUnavailable, ErrNoLane.Error()},
	}
	verbs := map[string]func(*Executor) error{
		"Execute": func(e *Executor) error { _, err := e.Execute(context.Background(), validReq()); return err },
		"ExecuteStream": func(e *Executor) error {
			_, _, err := e.ExecuteStream(context.Background(), validReq())
			return err
		},
	}
	for _, c := range cases {
		for verb, call := range verbs {
			t.Run(c.name+"/"+verb, func(t *testing.T) { assertSelectRefusal(t, c, call) })
		}
	}
}

// assertSelectRefusal runs call against an executor whose router refuses with
// c.selErr and checks the error's identity, kind and message, and that no
// provider was reached.
func assertSelectRefusal(t *testing.T, c quotaRefusalCase, call func(*Executor) error) {
	t.Helper()
	exec, deps := newReadyExecutor(t)
	deps.router.selectFn = func(context.Context, provider.ModelRequest, ...string) (provider.Selection, error) {
		return provider.Selection{}, c.selErr
	}
	var calls atomic.Int32
	deps.prov.chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		calls.Add(1)
		return provider.ChatResponse{}, nil
	}
	deps.prov.streamFn = func(context.Context, provider.ChatRequest, provider.StreamSink) error {
		calls.Add(1)
		return nil
	}
	err := call(exec)
	if err == nil {
		t.Fatal("a refused Select returned no error")
	}
	if !chainHolds(err, ErrNoLane) {
		t.Errorf("err %v does not carry ErrNoLane by identity", err)
	}
	if c.sentinel != nil && !chainHolds(err, c.sentinel) {
		t.Errorf("err %v does not carry the quota sentinel by identity", err)
	}
	if c.sentinel == nil && err != error(ErrNoLane) { //nolint:errorlint // identity is the assertion
		t.Errorf("a non-quota Select error became %v, want the bare ErrNoLane", err)
	}
	if !errors.Is(err, ErrNoLane) {
		t.Errorf("errors.Is(err, ErrNoLane) is false for %v", err)
	}
	if c.sentinel != nil && !errors.Is(err, c.sentinel) {
		t.Errorf("errors.Is(err, sentinel) is false for %v", err)
	}
	if k, _ := cascade.KindOf(err); k != c.kind {
		t.Errorf("kind = %v, want %v (%v)", k, c.kind, err)
	}
	if !strings.Contains(err.Error(), c.msg) {
		t.Errorf("message %q does not name %q", err.Error(), c.msg)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("provider called %d times after a refused Select", n)
	}
}

// chainHolds walks err's whole chain (single and multi unwrap) and reports
// whether it holds want itself, compared by pointer through errors.As.
func chainHolds(err error, want *cascade.Error) bool {
	var ce *cascade.Error
	if errors.As(err, &ce) && ce == want {
		return true
	}
	switch u := err.(type) { //nolint:errorlint // walking the chain by hand is the point
	case interface{ Unwrap() []error }:
		for _, inner := range u.Unwrap() {
			if chainHolds(inner, want) {
				return true
			}
		}
	case interface{ Unwrap() error }:
		if inner := u.Unwrap(); inner != nil {
			return chainHolds(inner, want)
		}
	}
	return false
}
