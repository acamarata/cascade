package main

// Purpose: unit tests for chat_resume_wiring.go -- wireChatResume and
//   newChatResumeEgressEngine, driven through chat_wiring_custody_test.go's
//   real chatRegistryForTest helper (the SAME wireChatHandlers call
//   production uses), never a second, parallel wiring path.
// SPORT: cmd/cascade chat resume wiring (TEST) -- P1-E22-W5-S47-T2.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// wiredCall dispatches method over registry and decodes the result into
// out, failing the test on an RPC error.
func wiredCall(t *testing.T, registry *rpc.Registry, method string, params, out any) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal %s: %v", method, err)
	}
	ctx := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true}) // the transport-verified owner CLI
	res, errObj := registry.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: method, Params: raw})
	if errObj != nil {
		t.Fatalf("%s over the real wiring: %+v", method, errObj)
	}
	b, err := json.Marshal(res)
	if err == nil {
		err = json.Unmarshal(b, out)
	}
	if err != nil {
		t.Fatalf("decode %s: %v", method, err)
	}
}

// TestChatResumeWiredInDaemon (contract name): the daemon's composition
// root (wireChatHandlers, the same call production makes) serves the whole
// resume flow -- create_thread, append_turn, resume -- through the wired
// Resumer (real Composer and egress engine). Failing input: dropping the
// SetResume call, which answers chat.resume with KindUnavailable.
func TestChatResumeWiredInDaemon(t *testing.T) {
	bindTestProcessFirewall(t)
	registry := chatRegistryForTest(t)
	var topic, session struct {
		ThreadID string `json:"thread_id"`
	}
	wiredCall(t, registry, "chat.create_thread", map[string]string{"privacy_mode": "internal"}, &topic)
	var appended map[string]any
	wiredCall(t, registry, "chat.append_turn", map[string]any{"thread_id": topic.ThreadID, "role": "user",
		"segments": []map[string]string{{"kind": "text", "content": "wired resume body"}}}, &appended)
	wiredCall(t, registry, "chat.create_thread", map[string]string{"privacy_mode": "restricted"}, &session)
	var resumed struct {
		ThreadID       string   `json:"thread_id"`
		LeadingContext string   `json:"leading_context"`
		Trimmed        []string `json:"trimmed"`
	}
	wiredCall(t, registry, "chat.resume", map[string]string{"topic_id": topic.ThreadID, "session_thread_id": session.ThreadID}, &resumed)
	if resumed.ThreadID != topic.ThreadID || !strings.Contains(resumed.LeadingContext, "wired resume body") || resumed.Trimmed == nil {
		t.Fatalf("chat.resume over the daemon wiring = %+v", resumed)
	}
}

// bindTestProcessFirewall binds the process firewall to a temp data
// directory over a file-vault custody, as the daemon's composition root
// does, and restores the previous binding when the test ends.
func bindTestProcessFirewall(t *testing.T) *processFirewall {
	t.Helper()
	resetProcessFirewall(t)
	selectProcessCustody = testProcessCustody(t.TempDir())
	fw, err := bindDaemonFirewall(firewallTestPaths(t))
	if err != nil {
		t.Fatalf("bindDaemonFirewall: %v", err)
	}
	return fw
}

// TestNewChatResumeEgressEngine_RealSubstitution proves the substitutor
// actually replaces a REAL vaulted value through the PROCESS engine: the
// failing input this guards is a passthrough stand-in, or a private engine
// over an empty vault, that returns content unchanged. The value is seeded
// through the process firewall's own broker, so only the engine
// egress.ProcessEngine hands out can find it.
func TestNewChatResumeEgressEngine_RealSubstitution(t *testing.T) {
	fw := bindTestProcessFirewall(t)
	const name, value = "chat_resume_wiring_test_secret", "sk-chat-resume-wiring-test-canary-0000000000000000"
	if _, err := fw.Broker().Set(context.Background(), name, []byte(value), secrets.SetUpdate); err != nil {
		t.Fatalf("seed vault: %v", err)
	}

	sub := newChatResumeEgressEngine()
	out, err := sub.InterceptClass(context.Background(), egress.EgressClassConversationResume, provider.SensitivityLocalOnly,
		[]byte("hello world "+value+" more text"))
	if err != nil {
		t.Fatalf("InterceptClass: %v", err)
	}
	if strings.Contains(string(out), value) {
		t.Fatalf("InterceptClass returned the seeded vault value verbatim: %q", out)
	}
	if !strings.Contains(string(out), "hello world") || !strings.Contains(string(out), "more text") {
		t.Fatalf("InterceptClass dropped the surrounding text along with the secret: %q", out)
	}
}

// TestNewChatResumeEgressEngine_NoFirewallFailsClosed: with no firewall
// factory installed the substitutor returns the engine's refusal and no
// content, so a resume cannot return an unsubstituted block.
func TestNewChatResumeEgressEngine_NoFirewallFailsClosed(t *testing.T) {
	egress.SetProcessEngineFactory(nil)
	t.Cleanup(func() { egress.SetProcessEngineFactory(processEngine) })
	out, err := newChatResumeEgressEngine().InterceptClass(context.Background(),
		egress.EgressClassConversationResume, provider.SensitivityPublic, []byte("payload"))
	if !errors.Is(err, egress.ErrNoProcessFirewall) || out != nil {
		t.Fatalf("InterceptClass = (%q, %v), want (nil, ErrNoProcessFirewall)", out, err)
	}
}

// resumeQueryTestMemoryLeg is a RecallWhatMemoryLeg double: one canned
// row, matching internal/retrieval's own baselineLegs() fixture shape
// (its test suite already proves this shape reaches a real fused answer).
type resumeQueryTestMemoryLeg struct{ rows []memory.IndexedRecord }

// SearchInScopeIncludingExpired (P1-E07-W5-S92-T1 interface rename, minimal
// hunk: this file's own lane is unrelated) ignores scopeRef, matching
// internal/retrieval's own fakeMemoryLeg double.
func (l resumeQueryTestMemoryLeg) SearchInScopeIncludingExpired(context.Context, string, string, int) ([]memory.IndexedRecord, error) {
	return l.rows, nil
}

// resumeQueryTestScopeResolver is a retrieval.ScopeResolver double
// returning a fixed, in-scope SessionScope regardless of input -- this
// test's Cwd never needs to name a real git checkout, since the point is
// proving dispatch reaches the real handler, not proving scope
// resolution itself (that is internal/context/scope's own test suite).
type resumeQueryTestScopeResolver struct{ out scope.SessionScope }

func (r resumeQueryTestScopeResolver) Resolve(context.Context, scope.ResolveInput) (scope.SessionScope, error) {
	return r.out, nil
}

// resumeQueryTestVault is an empty egress.Vault: no seeded value, so the
// exact-value substitution pass is a no-op and only the detector pass
// runs (neither fires on this test's non-secret content).
type resumeQueryTestVault struct{}

func (resumeQueryTestVault) List(context.Context) ([]string, error) { return nil, nil }
func (resumeQueryTestVault) Get(context.Context, string) ([]byte, error) {
	return nil, cascade.New(cascade.KindNotFound, "resumeQueryTestVault: empty")
}

// resumeQueryTestStore is a conversation.ResumeStore double with no
// threads: the query path must refuse before it would ever read one.
type resumeQueryTestStore struct{}

func (resumeQueryTestStore) GetThread(context.Context, string) (conversation.Thread, bool, error) {
	return conversation.Thread{}, false, nil
}
func (resumeQueryTestStore) ListTurns(context.Context, string) ([]conversation.Turn, error) {
	return nil, nil
}
func (resumeQueryTestStore) ListSegments(context.Context, string) ([]conversation.Segment, error) {
	return nil, nil
}
func (resumeQueryTestStore) ThreadPrivacy(context.Context, string) (provider.SensitivityTier, error) {
	return provider.SensitivityRestricted, nil
}

// resumeQueryTestConvLeg is a configured recall.what conversation leg whose
// store is down: every search answers unavailable.
type resumeQueryTestConvLeg struct{}

func (resumeQueryTestConvLeg) SearchTurns(context.Context, string, retrieval.RecallWhatSearchFilter) ([]retrieval.RecallWhatTurnMatch, error) {
	return nil, cascade.New(cascade.KindUnavailable, "conversation: search index unavailable")
}
func (resumeQueryTestConvLeg) ListSegments(context.Context, string) ([]retrieval.RecallWhatSegment, error) {
	return nil, nil
}
func (resumeQueryTestConvLeg) ThreadPrivacy(context.Context, string) (provider.SensitivityTier, error) {
	return provider.SensitivityRestricted, nil
}

// newResumeQueryResumer builds the production Resumer shape (real
// Composer, real resume egress engine) dispatching recall.what to a REAL
// RecallWhatService over a canned memory row, with or without a
// configured conversation leg.
func newResumeQueryResumer(t *testing.T, conv retrieval.RecallWhatConversationLeg) *conversation.Resumer {
	t.Helper()
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	mem := resumeQueryTestMemoryLeg{rows: []memory.IndexedRecord{
		{ID: "project/n1", Name: "n1", Kind: memory.KindProject, Body: "note about budget planning", ScopeRef: "proj1"},
	}}
	resolver := resumeQueryTestScopeResolver{out: scope.SessionScope{Kind: scope.ScopeKindSession, Project: "proj1"}}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	recallEgress, err := egress.NewEngine(egress.DefaultRegistry(), resumeQueryTestVault{}, detector)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	svc, err := retrieval.NewRecallWhatService(nil, conv, mem, rrf.Params{}, clock, resolver, recallEgress)
	if err != nil {
		t.Fatalf("NewRecallWhatService: %v", err)
	}
	registry := rpc.NewRegistry()
	retrieval.NewRecallWhatHandler(svc).Register(registry)
	composer, err := cascadecontext.NewComposer(provider.NaiveTokenCounter{}, nil)
	if err != nil {
		t.Fatalf("NewComposer: %v", err)
	}
	resumer, err := conversation.NewResumer(resumeQueryTestStore{}, composer, registry, newChatResumeEgressEngine())
	if err != nil {
		t.Fatalf("NewResumer: %v", err)
	}
	return resumer
}

// TestResumeQueryPath_DispatchesRealRecallWhatHandler proves the query
// path reaches the REAL recall.what handler over a real registry and is
// conversation-only: the memory row the service answers is never
// composed. With no conversation leg the result is KindNotFound; with a
// configured leg (production's shape, which reports itself unavailable)
// it is KindUnavailable.
func TestResumeQueryPath_DispatchesRealRecallWhatHandler(t *testing.T) {
	cases := map[string]struct {
		conv retrieval.RecallWhatConversationLeg
		kind cascade.Kind
	}{
		"no conversation leg":   {nil, cascade.KindNotFound},
		"conversation leg down": {resumeQueryTestConvLeg{}, cascade.KindUnavailable},
	}
	for name, c := range cases {
		res, err := newResumeQueryResumer(t, c.conv).Resume(context.Background(),
			conversation.ResumeRequest{Query: "budget planning", Cwd: "/nonexistent/does-not-matter"})
		if !cascade.HasKind(err, c.kind) || strings.Contains(res.Context, "budget planning") {
			t.Errorf("%s: Resume = %+v, %v; want %v and no memory row composed", name, res, err, c.kind)
		}
	}
}
