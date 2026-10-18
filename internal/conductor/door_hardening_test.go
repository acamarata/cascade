package conductor

// Purpose (this file): regression tests for the door's hardening findings -
//   a vault value that JSON would escape, the policy seeing the gate-resolved
//   tier, a stream Done that must not erase reported usage, an Execute
//   cancellation recording its cause, ExecuteStream's own audit action, and a
//   refusal that must not echo the caller's strings.
//
// SPORT: conductor.door (TEST) - P1-SEC-01.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// hostileValue is shapeless and carries every character JSON escapes on the
// way out: a quote, "&", "<", a backslash and a newline.
const hostileValue = "zq7\"canary&door<44\\71\nend"

type hostileVault struct{}

func (hostileVault) List(context.Context) ([]string, error) { return []string{canaryName}, nil }
func (hostileVault) Get(context.Context, string) ([]byte, error) {
	return []byte(hostileValue), nil
}

func TestDoorCanaryJSONHostileNeverLeaves(t *testing.T) {
	probe := "token is " + hostileValue + " ok"
	msgs := []provider.ChatMessage{{Role: "user", Content: probe}}
	ctx := context.Background()
	sel := provider.Selection{LaneID: "lane-1"}
	doors := map[string]func(*Executor) error{
		"Execute": func(e *Executor) error {
			_, err := e.Execute(ctx, provider.ModelRequest{TaskID: "t", TaskClass: "chat", Inputs: msgs})
			return err
		},
		"Chat": func(e *Executor) error {
			_, err := e.Chat(ctx, sel, provider.ChatRequest{Messages: msgs})
			return err
		},
		"Stream": func(e *Executor) error {
			return e.Stream(ctx, sel, provider.ChatRequest{Messages: msgs}, func(provider.StreamEvent) error { return nil })
		},
	}
	for name, call := range doors {
		t.Run(name, func(t *testing.T) {
			exec, seen := newCanaryExecutorFor(t, hostileVault{})
			if err := call(exec); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			got := seen.joined()
			if got == "" {
				t.Fatalf("%s: the recording provider was never called", name)
			}
			if strings.Contains(got, hostileValue) {
				t.Fatalf("%s: the JSON-hostile vault value reached the provider: %q", name, got)
			}
		})
	}
}

// tierPolicy records the tier Authorize saw and denies restricted requests.
type tierPolicy struct{ seen provider.SensitivityTier }

func (p *tierPolicy) Authorize(_ context.Context, r provider.ModelRequest) error {
	p.seen = r.Sensitivity
	if r.Sensitivity == provider.SensitivityRestricted {
		return errors.New("restricted is not allowed here")
	}
	return nil
}

func TestPolicySeesTheGateResolvedTier(t *testing.T) {
	cfg, _ := newReadyConfig(t)
	pol := &tierPolicy{}
	cfg.Policy, cfg.Sensitivity = pol, narrowGate{to: provider.SensitivityRestricted}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	req := validReq()
	req.Sensitivity = provider.SensitivityPublic
	_, err = exec.Execute(context.Background(), req)
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("Execute = %v, want the policy refusal of the narrowed tier", err)
	}
	if pol.seen != provider.SensitivityRestricted {
		t.Fatalf("policy saw %s, want the gate-resolved restricted", pol.seen)
	}
}

func TestStreamDoneWithoutUsageKeepsReportedUsage(t *testing.T) {
	exec, _, usage, provs := failoverRig(t)
	provs["lane-1"].streamFn = func(_ context.Context, _ provider.ChatRequest, sink provider.StreamSink) error {
		if err := sink(provider.StreamEvent{Kind: provider.StreamEventUsage, Usage: provider.Usage{InputTokens: 3, OutputTokens: 4}}); err != nil {
			return err
		}
		return sink(provider.StreamEvent{Kind: provider.StreamEventDone})
	}
	ch, _, err := exec.ExecuteStream(context.Background(), validReq())
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	collect(ch)
	if len(usage.recs) != 1 || usage.recs[0].TokensIn != 3 || usage.recs[0].TokensOut != 4 {
		t.Fatalf("usage = %+v, want one record of 3 in / 4 out", usage.recs)
	}
}

func TestExecuteCancellationAuditCarriesTheCause(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	started := make(chan struct{})
	deps.prov.chatFn = func(ctx context.Context, _ provider.ChatRequest) (provider.ChatResponse, error) {
		close(started)
		<-ctx.Done()
		return provider.ChatResponse{}, ctx.Err()
	}
	parent, stop := context.WithCancelCause(context.Background())
	go func() {
		<-started
		stop(errors.New("operator shutdown"))
	}()
	if _, err := exec.Execute(parent, validReq()); err == nil {
		t.Fatal("Execute on a cancelled context returned no error")
	}
	ev := deps.audit.events[len(deps.audit.events)-1]
	if ev.Outcome != "cancellation" || !strings.Contains(string(ev.Explain), "operator shutdown") {
		t.Fatalf("audit outcome %q explain %s, want a cancellation carrying the parent's cause", ev.Outcome, ev.Explain)
	}
}

func TestExecuteStreamAuditActionIsItsOwnVerb(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	deps.prov.streamFn = func(_ context.Context, _ provider.ChatRequest, sink provider.StreamSink) error {
		return sink(provider.StreamEvent{Kind: provider.StreamEventDone})
	}
	ch, _, err := exec.ExecuteStream(context.Background(), validReq())
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	collect(ch)
	if got := deps.audit.events[len(deps.audit.events)-1].Action; got != string(VerbExecuteStream) {
		t.Fatalf("terminal audit Action = %q, want %q", got, VerbExecuteStream)
	}
}

func TestLeafRefusalDoesNotEchoTheCallersStrings(t *testing.T) {
	cfg, deps := newReadyConfig(t)
	cfg.Router, cfg.Resolver = NewRouter(laneRegistry(), &fakeQuota{}, nil, nil), &selResolver{prov: deps.prov}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	for _, sel := range []provider.Selection{
		{LaneID: canaryValue},
		{Model: canaryValue},
		{LaneID: "lane-a", Provider: canaryValue},
	} {
		_, err := exec.Chat(context.Background(), sel, provider.ChatRequest{Messages: validReq().Inputs})
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("Chat(%+v) = %v, want KindInvalidInput", sel, err)
		}
		if strings.Contains(err.Error(), canaryValue) {
			t.Fatalf("the refusal echoes the caller's string: %v", err)
		}
	}
	for _, ev := range deps.audit.events {
		if strings.Contains(string(ev.Explain), canaryValue) {
			t.Fatalf("an audit record echoes the caller's string: %s", ev.Explain)
		}
	}
}

// wantViolation asserts err is the sensitivity refusal for lane at tier. A bare
// errors.Is would pass for any policy-denied error of the same Kind, so the
// message must also name the lane and the tier that was refused.
func wantViolation(t *testing.T, err error, lane, tier string) {
	t.Helper()
	if !errors.Is(err, ErrSensitivityViolation) {
		t.Fatalf("err = %v, want ErrSensitivityViolation", err)
	}
	if msg := err.Error(); !strings.Contains(msg, `"`+lane+`"`) || !strings.Contains(msg, tier) {
		t.Fatalf("refusal %q must name lane %q and tier %q", msg, lane, tier)
	}
}
