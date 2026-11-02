package plugins

import (
	"context"
	"os"
	"testing"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/cascade-pa/tools"
)

type callerCountingDoer struct {
	calls int
	err   error
}

func (d *callerCountingDoer) Do(context.Context, string, any, any) error {
	d.calls++
	return d.err
}

func TestCPAToolCallWithoutCallerRefused(t *testing.T) {
	for _, token := range []bool{false, true} {
		t.Setenv(principal.EnvToken, "test-principal")
		if !token {
			if err := os.Unsetenv(principal.EnvToken); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range []string{tools.ToolSend, tools.ToolHistory, tools.ToolSearch} {
			for _, state := range []string{"absent", "owner", "principal"} {
				checkToolCaller(t, name, state)
			}
		}
	}
}

func checkToolCaller(t *testing.T, name, state string) {
	t.Helper()
	stop := cascade.New(cascade.KindUnavailable, "recorded call")
	doer := &callerCountingDoer{err: stop}
	adapter := newCascadePAConversations(nil, 0, nil)
	adapter.doer = doer
	ctx := context.Background()
	if state == "owner" {
		ctx = principal.WithCaller(ctx, principal.Caller{})
	}
	if state == "principal" {
		ctx = principal.WithCaller(ctx, principal.Caller{Principal: &principal.Principal{ID: "ctx-principal"}})
	}
	input := map[string]string{tools.ToolSend: `{"content":"x"}`, tools.ToolHistory: `{}`, tools.ToolSearch: `{"query":"x"}`}
	_, err := tools.NewDispatcher(adapter).Dispatch(ctx, name, []byte(input[name]))
	if state == "owner" {
		if err != stop || err.Error() != "unavailable: recorded call" || doer.calls != 1 {
			t.Fatal(name, state, err, doer.calls)
		}
		return
	}
	message := "cascade-pa tool call has no caller"
	want := errToolNoCaller
	if state == "principal" {
		message = "cascade-pa tool call cannot forward its principal"
		want = errToolPrincipal
	}
	ce, ok := err.(*cascade.Error)
	if !ok || err != want || ce.Kind != cascade.KindPermissionDenied || ce.Msg != message || doer.calls != 0 {
		t.Fatal(name, state, err, doer.calls)
	}
}

// ownerToolCtx is the context a registry-stamped owner view carries into a
// cascade-pa tool call: a Caller with no principal.
func ownerToolCtx() context.Context {
	return principal.WithCaller(context.Background(), principal.Caller{})
}
