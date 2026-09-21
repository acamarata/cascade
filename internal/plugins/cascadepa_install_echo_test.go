package plugins

// Purpose (this file): unit coverage for cascadepa_install_echo.go --
//   renderInstallEventText's per-Kind rendering, chatEchoPublisher.Publish
//   driven by a fake rpcDoer (never a real socket -- internal/build's
//   no-network-unit-lane gate), and multiEventPublisher's errors.Join
//   combinator across all four echo/durable success/failure permutations.
// SPORT: internal/plugins:cascadepa-install-wiring (TEST) -- P1-E24-W5-S50-T4.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/plugins/cascade-pa/install"
)

// recordingDoer is an in-memory rpcDoer double -- the same seam
// cascadepa_wiring.go's own tests substitute (rpcDoer's own doc comment),
// so this file never reaches a real unix socket.
type recordingDoer struct {
	method string
	params any
	err    error
	calls  int
}

func (d *recordingDoer) Do(_ context.Context, method string, params, _ any) error {
	d.calls++
	d.method, d.params = method, params
	return d.err
}

func newTestChatEchoPublisher(doer rpcDoer) *chatEchoPublisher {
	p := newChatEchoPublisher(func() (runtime.PathProvider, error) { return nil, errors.New("unused") })
	p.client.doer = doer
	return p
}

// testEchoThreadID is the fixture thread id every echo-delivery test below
// sets on its Event -- without one, Publish now (T0 decision D4) skips the
// echo entirely rather than minting a fresh thread; see
// TestChatEchoPublisher_EmptyThreadIDSkipsEchoNeverMintsOne for that branch.
const testEchoThreadID = "thread-abc123"

func TestChatEchoPublisher_PublishesUnderRealWireMethod(t *testing.T) {
	doer := &recordingDoer{}
	p := newTestChatEchoPublisher(doer)
	evt := install.Event{Kind: install.EventInstallProposal, ThreadID: testEchoThreadID,
		Proposal: &install.Proposal{PluginID: "gh", Name: "GitHub", Version: "1.0.0", Intent: "git push"}}
	if err := p.Publish(context.Background(), evt); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if doer.calls != 1 {
		t.Fatalf("rpc.Do called %d times, want 1", doer.calls)
	}
	if doer.method != conversation.MethodAppendTurn {
		t.Errorf("method = %q, want %q", doer.method, conversation.MethodAppendTurn)
	}
	params, ok := doer.params.(appendTurnParams)
	if !ok {
		t.Fatalf("params = %T, want appendTurnParams", doer.params)
	}
	// T0 decision D4: the appended turn must land on the RUN's own
	// originating thread, never a freshly minted one.
	if params.ThreadID != testEchoThreadID {
		t.Errorf("ThreadID = %q, want the event's own originating thread %q", params.ThreadID, testEchoThreadID)
	}
	if params.Role != "system" {
		t.Errorf("Role = %q, want %q (a host-authored notice, never the operator's own words)", params.Role, "system")
	}
	if len(params.Segments) != 1 || !strings.Contains(params.Segments[0].Content, "GitHub") {
		t.Errorf("Segments = %+v, want one segment mentioning the proposal's Name", params.Segments)
	}
}

func TestChatEchoPublisher_TransportFailurePropagates(t *testing.T) {
	wantErr := cascade.New(cascade.KindUnavailable, "boom: socket unreachable")
	p := newTestChatEchoPublisher(&recordingDoer{err: wantErr})
	if err := p.Publish(context.Background(), install.Event{Kind: install.EventInstallDeclined, ThreadID: testEchoThreadID}); !errors.Is(err, wantErr) {
		t.Fatalf("Publish: err = %v, want it to wrap %v", err, wantErr)
	}
}

func TestChatEchoPublisher_PathResolutionFailurePropagates(t *testing.T) {
	wantErr := errors.New("boom: no home directory")
	p := newChatEchoPublisher(func() (runtime.PathProvider, error) { return nil, wantErr })
	if err := p.Publish(context.Background(), install.Event{Kind: install.EventInstallFailed, ThreadID: testEchoThreadID}); !errors.Is(err, wantErr) {
		t.Fatalf("Publish: err = %v, want it to wrap %v", err, wantErr)
	}
}

// TestChatEchoPublisher_EmptyThreadIDSkipsEchoNeverMintsOne is the round-2
// rework proof (T0 decision D4): an Event with no ThreadID must never
// reach rpc.Do at all -- an empty appendTurnParams.ThreadID mints a FRESH
// conversation thread per call (internal/conversation/privacy.go's
// resolveAppendThread), which would scatter one install run's events
// across a new thread each. The path-resolution seam is deliberately
// broken (errors.New("unused") never called) to prove Publish returns
// before it ever needs a real client.
func TestChatEchoPublisher_EmptyThreadIDSkipsEchoNeverMintsOne(t *testing.T) {
	doer := &recordingDoer{}
	p := newTestChatEchoPublisher(doer)
	if err := p.Publish(context.Background(), install.Event{Kind: install.EventInstallProposal}); err != nil {
		t.Fatalf("Publish: %v, want nil (skip is not a failure)", err)
	}
	if doer.calls != 0 {
		t.Fatalf("rpc.Do called %d times, want 0 -- an empty thread id must never reach the transport", doer.calls)
	}
}

func TestRenderInstallEventText_EveryKind(t *testing.T) {
	cases := []struct {
		name string
		evt  install.Event
		want string
	}{
		{"proposal-nil-payload", install.Event{Kind: install.EventInstallProposal}, "proposing"},
		{"proposal", install.Event{Kind: install.EventInstallProposal,
			Proposal: &install.Proposal{PluginID: "gh", Name: "GitHub", Version: "1.0.0", Intent: "git push"}}, "GitHub"},
		{"declined-nil-payload", install.Event{Kind: install.EventInstallDeclined}, "declined"},
		{"declined", install.Event{Kind: install.EventInstallDeclined,
			Declined: &install.Declined{PluginID: "gh", Reason: install.DeclineExplicit}}, "explicit"},
		{"failed-nil-payload", install.Event{Kind: install.EventInstallFailed}, "failed"},
		{"failed", install.Event{Kind: install.EventInstallFailed,
			Failed: &install.Failed{PluginID: "gh", Reason: cascade.KindUnavailable}}, "unavailable"},
		{"elevation-nil-payload", install.Event{Kind: install.EventElevationRequired}, "elevation"},
		{"elevation", install.Event{Kind: install.EventElevationRequired,
			Elevation: &install.ElevationRequired{PluginID: "agent-runner", Runtime: plugin.RuntimeProcess}}, "agent-runner"},
		{"resume-nil-payload", install.Event{Kind: install.EventConversationResume}, "resuming"},
		{"resume-fresh", install.Event{Kind: install.EventConversationResume,
			Resume: &install.ResumeEvent{PluginID: "gh", Intent: "git push"}}, "installed"},
		{"resume-already", install.Event{Kind: install.EventConversationResume,
			Resume: &install.ResumeEvent{PluginID: "gh", Intent: "git push", AlreadyInstalled: true}}, "already installed"},
		{"unknown-kind", install.Event{Kind: install.EventKind("something.else")}, "something.else"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := renderInstallEventText(c.evt)
			if !strings.Contains(got, c.want) {
				t.Errorf("renderInstallEventText(%+v) = %q, want it to contain %q", c.evt, got, c.want)
			}
		})
	}
}

func TestMultiEventPublisher_BothSucceed(t *testing.T) {
	doer := &recordingDoer{}
	rec := &recordingEventPublisher{}
	m := &multiEventPublisher{echo: newTestChatEchoPublisher(doer), durable: newBusEventPublisher(rec)}
	if err := m.Publish(context.Background(), install.Event{Kind: install.EventInstallProposal, ThreadID: testEchoThreadID}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if doer.calls != 1 || rec.calls != 1 {
		t.Fatalf("echo calls=%d durable calls=%d, want 1 and 1 -- both halves must always be attempted", doer.calls, rec.calls)
	}
}

func TestMultiEventPublisher_EchoFailureStillAttemptsDurable(t *testing.T) {
	wantErr := errors.New("boom: echo transport down")
	doer := &recordingDoer{err: wantErr}
	rec := &recordingEventPublisher{}
	m := &multiEventPublisher{echo: newTestChatEchoPublisher(doer), durable: newBusEventPublisher(rec)}
	err := m.Publish(context.Background(), install.Event{Kind: install.EventInstallProposal, ThreadID: testEchoThreadID})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Publish: err = %v, want it to wrap %v", err, wantErr)
	}
	if rec.calls != 1 {
		t.Fatalf("durable.Publish called %d times, want 1 -- an echo failure must never suppress the durable record", rec.calls)
	}
}

func TestMultiEventPublisher_DurableFailureStillAttemptedEcho(t *testing.T) {
	wantErr := errors.New("boom: store unavailable")
	doer := &recordingDoer{}
	rec := &recordingEventPublisher{err: wantErr}
	m := &multiEventPublisher{echo: newTestChatEchoPublisher(doer), durable: newBusEventPublisher(rec)}
	err := m.Publish(context.Background(), install.Event{Kind: install.EventInstallProposal, ThreadID: testEchoThreadID})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Publish: err = %v, want it to wrap %v", err, wantErr)
	}
	if doer.calls != 1 {
		t.Fatalf("echo.Publish called %d times, want 1 -- a durable-bus failure must never suppress the echo attempt", doer.calls)
	}
}

func TestMultiEventPublisher_BothFailJoinsBothErrors(t *testing.T) {
	echoErr := errors.New("boom: echo down")
	durableErr := errors.New("boom: bus down")
	m := &multiEventPublisher{
		echo:    newTestChatEchoPublisher(&recordingDoer{err: echoErr}),
		durable: newBusEventPublisher(&recordingEventPublisher{err: durableErr}),
	}
	err := m.Publish(context.Background(), install.Event{Kind: install.EventInstallProposal, ThreadID: testEchoThreadID})
	if !errors.Is(err, echoErr) || !errors.Is(err, durableErr) {
		t.Fatalf("Publish: err = %v, want it to wrap BOTH %v and %v", err, echoErr, durableErr)
	}
}
