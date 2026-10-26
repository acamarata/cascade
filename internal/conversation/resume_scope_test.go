package conversation_test

// Purpose: chat.resume under caller-principal scoping (owner-view): a
//   principal resumes only between threads attributed to its own project
//   scope, on both the topic path and the query path; the owner resumes
//   between any threads. Runs over the composed registry with the access
//   policy on, a real Resumer and a real Composer.
// Inputs: threads written only through chat.create_thread and
//   chat.append_turn by minted principals (the shared chatFixture).
// Outputs: test verdicts only.
// SPORT: internal.conversation.adapter (chat.resume scope).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// passthroughEgress is an EgressSubstitutor that returns content unchanged.
type passthroughEgress struct{}

func (passthroughEgress) InterceptClass(_ context.Context, _ egress.EgressClass, _ provider.SensitivityTier, content []byte) ([]byte, error) {
	return content, nil
}

// fixedRecall answers every recall.what dispatch with one conversation hit.
type fixedRecall struct{ path string }

func (f fixedRecall) Dispatch(context.Context, *rpc.Request) (any, *rpc.ErrorObject) {
	return map[string]any{"results": []map[string]any{{"rank": 1, "domain": "conversation", "path": f.path}}}, nil
}

// newResumeScopeFixture is newChatFixture with chat.resume wired over a real
// Resumer whose query leg answers path.
func newResumeScopeFixture(t *testing.T, path *string) *chatFixture {
	t.Helper()
	f := newChatFixture(t)
	composer, err := cascadecontext.NewComposer(provider.NaiveTokenCounter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	querier := resumeQuerierFunc(func(ctx context.Context, req *rpc.Request) (any, *rpc.ErrorObject) {
		return fixedRecall{path: *path}.Dispatch(ctx, req)
	})
	resumer, err := conversation.NewResumer(f.store, composer, querier, passthroughEgress{})
	if err != nil {
		t.Fatal(err)
	}
	clock := testkit.NewFrozenClock(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))
	adapter := conversation.NewAdapter(f.store, nil, nil, clock, conversation.ModeEmbedded)
	adapter.SetResume(resumer)
	f.reg = rpc.NewRegistry()
	f.reg.SetAccessPolicy(func(token string) (principal.Principal, error) { return f.minter.Resolve(token, f.clock.Now()) })
	adapter.RegisterHandlers(f.reg)
	return f
}

// resumeQuerierFunc adapts a function to conversation.ResumeQuerier.
type resumeQuerierFunc func(context.Context, *rpc.Request) (any, *rpc.ErrorObject)

func (fn resumeQuerierFunc) Dispatch(ctx context.Context, req *rpc.Request) (any, *rpc.ErrorObject) {
	return fn(ctx, req)
}

// createThreadAs creates a thread through chat.create_thread as ctx.
func (f *chatFixture) createThreadAs(ctx context.Context, t *testing.T, mode string) string {
	t.Helper()
	out, eo := f.call(ctx, "chat.create_thread", map[string]string{"privacy_mode": mode})
	if eo != nil {
		t.Fatalf("chat.create_thread: %+v", eo)
	}
	var res struct {
		ThreadID string `json:"thread_id"`
	}
	if err := json.Unmarshal(out, &res); err != nil || res.ThreadID == "" {
		t.Fatalf("chat.create_thread result %s: %v", out, err)
	}
	return res.ThreadID
}

// TestResumeRefusesForeignScopeForPrincipal: a principal resumes only
// between threads attributed to its own scope. A foreign source (topic or
// query hit) reads as not found and composes nothing; a foreign target is
// refused outright; the owner resumes across scopes. Failing input: a
// handler that resolves tiers from the store without the caller's view.
func TestResumeRefusesForeignScopeForPrincipal(t *testing.T) {
	var queryPath string
	f := newResumeScopeFixture(t, &queryPath)
	alpha, beta := f.as(t, "project:alpha"), f.as(t, "project:beta")
	alphaThread := f.appendAs(alpha, t, "", "alpha notes")
	betaThread := f.appendAs(beta, t, "", "beta secrets")
	alphaSession := f.createThreadAs(alpha, t, "restricted")
	betaSession := f.createThreadAs(beta, t, "restricted")

	resume := func(ctx context.Context, params map[string]string) (string, *rpc.ErrorObject) {
		out, eo := f.call(ctx, "chat.resume", params)
		return string(out), eo
	}
	if out, eo := resume(alpha, map[string]string{"topic_id": alphaThread, "session_thread_id": alphaSession}); eo != nil || !strings.Contains(out, "alpha notes") {
		t.Fatalf("own scope resume = %s, %+v; want the block", out, eo)
	}
	assertNothingComposed := func(name, out string, eo *rpc.ErrorObject, want cascade.Kind) {
		t.Helper()
		if eo == nil || eo.Code != want.JSONRPCCode() || strings.Contains(out, "beta secrets") || strings.Contains(eo.Message, "beta secrets") {
			t.Errorf("%s = %s, %+v; want %v and no foreign content", name, out, eo, want)
		}
	}
	out, eo := resume(alpha, map[string]string{"topic_id": betaThread, "session_thread_id": alphaSession})
	assertNothingComposed("foreign topic", out, eo, cascade.KindNotFound)
	queryPath = betaThread
	out, eo = resume(alpha, map[string]string{"query": "secrets", "session_thread_id": alphaSession})
	assertNothingComposed("foreign query hit", out, eo, cascade.KindNotFound)
	if strings.Contains(eo.Message, betaThread) {
		t.Errorf("foreign query hit refusal names the foreign thread: %q", eo.Message)
	}
	out, eo = resume(alpha, map[string]string{"topic_id": alphaThread, "session_thread_id": betaSession})
	assertNothingComposed("foreign target", out, eo, cascade.KindPermissionDenied)
	if out, eo := resume(ownerCtx(), map[string]string{"topic_id": betaThread, "session_thread_id": alphaSession}); eo != nil || !strings.Contains(out, "beta secrets") {
		t.Fatalf("owner cross-scope resume = %s, %+v; want the block", out, eo)
	}
}
