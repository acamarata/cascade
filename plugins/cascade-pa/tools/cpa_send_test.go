package tools

// Purpose (this file): cascade_cpa_send's behaviour — what reaches the
//   conversation service, what comes back, and the one direction an
//   unrecognised sensitivity may resolve in.
// SPORT: plugins/cascade-pa:mcp-tools:cascade_cpa_send (TEST) — P1-E20-W5-S43-T4.

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestSendStartsAThreadWhenNoneIsNamed(t *testing.T) {
	svc := &fakeConversations{appendResult: AppendResult{
		ThreadID: "thr-minted-by-the-server", TurnID: "turn-1", CreatedAt: "2026-09-20T12:00:00Z",
	}}
	d := NewDispatcher(svc)

	var out SendOutput
	dispatchJSON(t, d, ToolSend, `{"content":"hello"}`, &out)

	if len(svc.gotAppend) != 1 {
		t.Fatalf("Append called %d times, want 1", len(svc.gotAppend))
	}
	if got := svc.gotAppend[0].ThreadID; got != "" {
		t.Errorf("Append thread_id = %q, want empty so the server mints one", got)
	}
	if got := svc.gotAppend[0].Role; got != "user" {
		t.Errorf("Append role = %q, want \"user\"", got)
	}
	if got := svc.gotAppend[0].Content; got != "hello" {
		t.Errorf("Append content = %q, want \"hello\"", got)
	}
	// R-14.285: the id the SERVER minted, never the empty one the caller
	// sent. An agent that got its own "" back could not continue the
	// thread it had just started.
	if out.ThreadID != "thr-minted-by-the-server" {
		t.Errorf("thread_id = %q, want the server's", out.ThreadID)
	}
	if out.TurnID != "turn-1" || out.CreatedAt != "2026-09-20T12:00:00Z" {
		t.Errorf("turn_id/created_at = %q/%q, want turn-1/2026-09-20T12:00:00Z", out.TurnID, out.CreatedAt)
	}
}

func TestSendContinuesANamedThread(t *testing.T) {
	svc := &fakeConversations{appendResult: AppendResult{ThreadID: "thr-7", TurnID: "turn-9"}}
	d := NewDispatcher(svc)

	var out SendOutput
	dispatchJSON(t, d, ToolSend, `{"content":"more","thread_id":"thr-7"}`, &out)

	if got := svc.gotAppend[0].ThreadID; got != "thr-7" {
		t.Errorf("Append thread_id = %q, want thr-7 passed through", got)
	}
	if out.ThreadID != "thr-7" {
		t.Errorf("thread_id = %q, want thr-7", out.ThreadID)
	}
}

func TestSendResolvesSensitivityFailClosed(t *testing.T) {
	// 06 §5.16: the enum has no permissive zero value. Unset, unknown,
	// misspelled and differently-cased all resolve to local-only. The
	// table below deliberately includes a case-variant of a VALID tier —
	// "Public" must not widen, because a resolution that accepted it
	// would be a loosening nobody authorised.
	cases := []struct{ in, want string }{
		{"", SensitivityLocalOnly},
		{"restricted", SensitivityRestricted},
		{"local-only", SensitivityLocalOnly},
		{"internal", SensitivityInternal},
		{"public", SensitivityPublic},
		{"Public", SensitivityLocalOnly},
		{"PUBLIC", SensitivityLocalOnly},
		{" public", SensitivityLocalOnly},
		{"publik", SensitivityLocalOnly},
		{"secret", SensitivityLocalOnly},
		{"local_only", SensitivityLocalOnly},
	}
	for _, c := range cases {
		if got := ResolveSensitivity(c.in); got != c.want {
			t.Errorf("ResolveSensitivity(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSendEchoesTheResolvedSensitivity(t *testing.T) {
	// Echoed, not assumed: a caller that misspelled a tier is TOLD its
	// request was narrowed, rather than believing its public turn is
	// public.
	svc := &fakeConversations{appendResult: AppendResult{ThreadID: "t", TurnID: "u"}}
	d := NewDispatcher(svc)
	var out SendOutput
	dispatchJSON(t, d, ToolSend, `{"content":"x","sensitivity":"publik"}`, &out)
	if out.Sensitivity != SensitivityLocalOnly {
		t.Errorf("sensitivity = %q, want %q echoed back", out.Sensitivity, SensitivityLocalOnly)
	}
}

func TestResolveSensitivityUnknownIsLocalOnly(t *testing.T) {
	TestSendResolvesSensitivityFailClosed(t)
	svc := &fakeConversations{appendResult: AppendResult{ThreadID: "t", TurnID: "u"}}
	var out SendOutput
	dispatchJSON(t, NewDispatcher(svc), ToolSend, `{"content":"x"}`, &out)
	if len(svc.gotAppend) != 1 || svc.gotAppend[0].Sensitivity != SensitivityLocalOnly || out.Sensitivity != SensitivityLocalOnly {
		t.Fatal("unset sensitivity did not stay local-only", svc.gotAppend, out)
	}
}

func TestSendRefusesEmptyContent(t *testing.T) {
	svc := &fakeConversations{appendResult: AppendResult{ThreadID: "t", TurnID: "u"}}
	d := NewDispatcher(svc)
	err := dispatchErr(t, d, ToolSend, `{"content":""}`)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("empty content: err = %v, want KindInvalidInput", err)
	}
	if len(svc.gotAppend) != 0 {
		t.Fatal("an empty turn reached the conversation service")
	}
}

func TestSendRefusesWhenTheServiceNamesNoThread(t *testing.T) {
	// A service that recorded a turn and named no thread has left the
	// caller unable to continue it. Reporting success here would hand an
	// agent a thread id of "" to page with.
	svc := &fakeConversations{appendResult: AppendResult{TurnID: "turn-1"}}
	d := NewDispatcher(svc)
	err := dispatchErr(t, d, ToolSend, `{"content":"hello"}`)
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("no thread id: err = %v, want KindInternal", err)
	}
	if !strings.Contains(err.Error(), "named no thread") {
		t.Errorf("err = %v, want it to say the service named no thread", err)
	}
}

func TestSendPropagatesTheServiceError(t *testing.T) {
	// The service's own taxonomy, unmodified: a daemon that is not
	// running must not be reported as invalid input.
	svc := &fakeConversations{appendErr: cascade.New(cascade.KindUnavailable, "daemon not running")}
	d := NewDispatcher(svc)
	err := dispatchErr(t, d, ToolSend, `{"content":"hello"}`)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("service failure: err = %v, want KindUnavailable preserved", err)
	}
}
