package daemon

// Purpose: proves the in-process conductor door (conductor_door.go): binding
//   states, Embed's tier and lane checks, the Executor door, deny, quota,
//   rebind, and the caller's ctx reaching Router.Select unchanged.
// SPORT: internal/daemon (tests, P1-CPA-10).

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// doorProvider counts every provider verb the door could reach.
type doorProvider struct {
	provider.ModelProvider
	calls atomic.Int32
}

func (p *doorProvider) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	p.calls.Add(1)
	return provider.ChatResponse{Message: provider.ChatMessage{Role: "assistant", Content: "ok"}, FinishReason: "stop"}, nil
}
func (p *doorProvider) Embed(_ context.Context, req provider.ModelEmbedRequest) (provider.ModelEmbedResponse, error) {
	p.calls.Add(1)
	return provider.ModelEmbedResponse{Vectors: [][]float32{{0.5}, {1.5}}[:len(req.Inputs)]}, nil
}
func (p *doorProvider) Resolve(context.Context, provider.Selection) (provider.ModelProvider, error) {
	return p, nil
}

// doorLanes is a registry with one healthy external lane, "mem"; doorQuota picks it.
type doorLanes struct{}
type doorQuota struct{}

var memInfo = provider.ProviderInfo{Name: "mem", HealthStatus: "healthy", BaseURL: "https://mem.example.invalid"}

func (doorLanes) GetProvider(context.Context, string) (provider.ProviderInfo, error) {
	return memInfo, nil
}
func (doorLanes) ListProviders(context.Context) ([]provider.ProviderInfo, error) {
	return []provider.ProviderInfo{memInfo}, nil
}
func (doorLanes) ListLanes(context.Context) ([]provider.LaneInfo, error) {
	return []provider.LaneInfo{{LaneName: "mem", ProviderName: "mem"}}, nil
}
func (doorLanes) ListPool(context.Context, string) ([]provider.LaneInfo, error) { return nil, nil }
func (doorLanes) GetByModel(context.Context, string) ([]provider.ProviderInfo, error) {
	return nil, nil
}
func (doorQuota) NextLane(context.Context, []conductor.LaneID) (conductor.LaneID, error) {
	return "mem", nil
}

// doorAudit records every appended event. Every audited path in these tests
// appends on the calling goroutine.
type doorAudit struct{ events []audit.Event }

func (a *doorAudit) Append(_ context.Context, ev audit.Event) (audit.Record, error) {
	a.events = append(a.events, ev)
	return audit.Record{}, nil
}
func (a *doorAudit) count(action, verdict string) (n int) {
	for _, ev := range a.events {
		if ev.Action == action && (verdict == "" || ev.Verdict == verdict) {
			n++
		}
	}
	return n
}

// denyCode denies task class "code" the way the daemon's owner deny store does.
type denyCode struct{}

func (denyCode) DeniedTaskClass(_ context.Context, c string) (string, bool) {
	return "denied by [conductor].deny_task_classes", c == "code"
}

// bindDoor composes a conductor over p, binding the process door.
func bindDoor(t *testing.T, p *doorProvider, quota conductor.QuotaSpiller, sec ConductorSecurity) *doorAudit {
	t.Helper()
	aw, clock := &doorAudit{}, runtime.NewSystemClock()
	if err := RegisterConductorExecuteHandler(rpc.NewRegistry(), NewManifest(nil, clock), doorLanes{}, quota, p,
		aw, clock, sec, ConductorAccounting{Reserver: passReserver{}}, ConductorFanOut{}); err != nil {
		t.Fatal(err)
	}
	return aw
}

// doorChainHolds walks err's chain for want by pointer identity.
func doorChainHolds(err error, want error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if err == want { //nolint:errorlint // identity is the assertion
			return true
		}
	}
	return false
}

// doorMethodErrors calls every door method and returns their errors.
func doorMethodErrors(d *ConductorDoor) map[string]error {
	ctx := context.Background()
	_, embedErr := d.Embed(ctx, "mem", "m", provider.SensitivityInternal, []string{"x"})
	_, execErr := d.Execute(ctx, doorReq("chat"))
	_, _, streamErr := d.ExecuteStream(ctx, doorReq("chat"))
	return map[string]error{"Ready": d.Ready(), "Embed": embedErr, "Execute": execErr, "ExecuteStream": streamErr}
}

func doorReq(class string) provider.ModelRequest {
	return provider.ModelRequest{TaskID: "t1", TaskClass: class, Sensitivity: provider.SensitivityPublic,
		Inputs: []provider.ChatMessage{{Role: "user", Content: "hello"}}}
}

func TestConductorDoorUnboundUnavailable(t *testing.T) {
	door := ProcessConductorDoor()
	if door == nil || door != ProcessConductorDoor() {
		t.Fatal("ProcessConductorDoor is not one stable non-nil pointer")
	}
	for name, err := range doorMethodErrors(&ConductorDoor{}) {
		k, _ := cascade.KindOf(err)
		if err != errConductorDoorNotBound || k != cascade.KindUnavailable || !strings.HasSuffix(err.Error(), ": conductor door not bound") { //nolint:errorlint // identity
			t.Errorf("%s on an unbound door = %v, want errConductorDoorNotBound", name, err)
		}
	}
}

func TestConductorDoorExecutorUnavailable(t *testing.T) {
	clock := runtime.NewSystemClock()
	if err := RegisterConductorExecuteHandler(rpc.NewRegistry(), NewManifest(nil, clock), doorLanes{}, doorQuota{}, nil,
		&doorAudit{}, clock, fullSecurity(t), ConductorAccounting{}, ConductorFanOut{}); err != nil {
		t.Fatal(err)
	}
	for name, err := range doorMethodErrors(ProcessConductorDoor()) {
		k, _ := cascade.KindOf(err)
		if k != cascade.KindUnavailable || !doorChainHolds(err, conductor.ErrConstructionFailed) || !strings.Contains(err.Error(), conductor.ErrConstructionFailed.Error()) {
			t.Errorf("%s = %v, want KindUnavailable wrapping and naming ErrConstructionFailed", name, err)
		}
	}
}

// assertTierRefused embeds at tier and expects a pre-dispatch refusal.
func assertTierRefused(t *testing.T, tier provider.SensitivityTier) {
	t.Helper()
	p := &doorProvider{}
	aw := bindDoor(t, p, doorQuota{}, fullSecurity(t))
	_, err := ProcessConductorDoor().Embed(context.Background(), "mem", "m", tier, []string{"secret text"})
	if k, _ := cascade.KindOf(err); k != cascade.KindPolicyDenied || !strings.Contains(err.Error(), "embedding refused") {
		t.Fatalf("Embed at %v = %v, want the door's KindPolicyDenied refusal", tier, err)
	}
	if n := p.calls.Load(); n != 0 {
		t.Errorf("provider called %d times for a refused tier", n)
	}
	if n, all := aw.count(string(conductor.VerbEmbed), "refusal"), len(aw.events); n != 1 || all != 1 {
		t.Errorf("audit = %d refusal records of %d, want exactly one", n, all)
	}
}

func TestConductorDoorRefusesLocalOnlyTier(t *testing.T) {
	assertTierRefused(t, provider.SensitivityLocalOnly)
}

func TestConductorDoorRefusesRestrictedTier(t *testing.T) {
	assertTierRefused(t, provider.SensitivityRestricted)
	assertTierRefused(t, provider.SensitivityTier(9))
}

func TestConductorDoorUnknownLaneNotFound(t *testing.T) {
	p := &doorProvider{}
	bindDoor(t, p, doorQuota{}, fullSecurity(t))
	for _, lane := range []string{"other", "MEM", "me", ""} {
		_, err := ProcessConductorDoor().Embed(context.Background(), lane, "m", provider.SensitivityInternal, []string{"x"})
		if k, _ := cascade.KindOf(err); k != cascade.KindNotFound {
			t.Errorf("Embed on lane %q = %v, want KindNotFound", lane, err)
		}
	}
	if p.calls.Load() != 0 {
		t.Error("provider called for an unknown lane")
	}
}

func TestConductorDoorEmbedDispatchesThroughExecutor(t *testing.T) {
	p := &doorProvider{}
	aw := bindDoor(t, p, doorQuota{}, fullSecurity(t))
	vecs, err := ProcessConductorDoor().Embed(context.Background(), "mem", "m", provider.SensitivityInternal, []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 || vecs[1][0] != 1.5 || p.calls.Load() != 1 {
		t.Errorf("vectors = %v after %d provider calls, want two vectors from exactly one call", vecs, p.calls.Load())
	}
	if n := aw.count(string(conductor.VerbEmbed), ""); n != 1 {
		t.Errorf("model.embed audit records = %d, want the Executor's one record", n)
	}
	if aw.events[0].RiskLevel != provider.SensitivityInternal.String() {
		t.Errorf("audit tier = %q, want the door tier %q", aw.events[0].RiskLevel, provider.SensitivityInternal)
	}
}

// assertBothVerbsRefused expects Execute and ExecuteStream to refuse before dispatch.
func assertBothVerbsRefused(t *testing.T, p *doorProvider, class string, kind cascade.Kind, msg string) {
	t.Helper()
	d := ProcessConductorDoor()
	_, execErr := d.Execute(context.Background(), doorReq(class))
	_, _, streamErr := d.ExecuteStream(context.Background(), doorReq(class))
	for verb, err := range map[string]error{"Execute": execErr, "ExecuteStream": streamErr} {
		if k, _ := cascade.KindOf(err); k != kind || (err == nil || !strings.Contains(err.Error(), msg)) {
			t.Errorf("%s = %v, want %v naming %q", verb, err, kind, msg)
		}
	}
	if n := p.calls.Load(); n != 0 {
		t.Errorf("provider called %d times after a refusal", n)
	}
}

func TestConductorDoorExecuteHonoursOwnerDeny(t *testing.T) {
	p := &doorProvider{}
	sec := fullSecurity(t)
	sec.Policy = conductor.NewOwnerPolicy(denyCode{})
	bindDoor(t, p, doorQuota{}, sec)
	assertBothVerbsRefused(t, p, "code", cascade.KindPolicyDenied, `task class "code" is denied on this machine: denied by [conductor].deny_task_classes`)
	if _, err := ProcessConductorDoor().Execute(context.Background(), doorReq("chat")); err != nil || p.calls.Load() != 1 {
		t.Errorf("an allowed class = %v with %d calls, want one dispatch", err, p.calls.Load())
	}
}

func TestConductorDoorExecuteHonoursQuota(t *testing.T) {
	cfg := conductor.QuotaConfig{SpillOrder: []conductor.LaneID{"mem"}, CeilingOverrides: map[conductor.LaneID]int64{"mem": 0}}
	p := &doorProvider{}
	bindDoor(t, p, conductor.NewQuotaPolicy(cfg, runtime.NewSystemClock()), fullSecurity(t))
	assertBothVerbsRefused(t, p, "chat", cascade.KindQuotaExhausted, "ceiling reached for: mem")
}

func TestConductorDoorRebindReplacesExecutor(t *testing.T) {
	first, second := &doorProvider{}, &doorProvider{}
	bindDoor(t, first, doorQuota{}, fullSecurity(t))
	bindDoor(t, second, doorQuota{}, fullSecurity(t))
	if _, err := ProcessConductorDoor().Embed(context.Background(), "mem", "m", provider.SensitivityPublic, []string{"x"}); err != nil {
		t.Fatalf("Embed after rebind: %v", err)
	}
	if first.calls.Load() != 0 || second.calls.Load() != 1 {
		t.Errorf("calls first=%d second=%d, want the second composition's Executor only", first.calls.Load(), second.calls.Load())
	}
}

// ctxRouter records what Router.Select saw, then refuses.
type ctxRouter struct{ seen chan context.Context }
type doorCtxKey struct{}

func (r ctxRouter) Select(ctx context.Context, _ provider.ModelRequest, _ ...string) (provider.Selection, error) {
	r.seen <- ctx
	return provider.Selection{}, cascade.New(cascade.KindUnavailable, "recording router selects nothing")
}

func TestDoorPreservesCallerContext(t *testing.T) {
	sec := fullSecurity(t)
	calls := map[string]func(*ConductorDoor, context.Context){
		"Execute":       func(d *ConductorDoor, ctx context.Context) { _, _ = d.Execute(ctx, doorReq("chat")) },
		"ExecuteStream": func(d *ConductorDoor, ctx context.Context) { _, _, _ = d.ExecuteStream(ctx, doorReq("chat")) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			router, aw := ctxRouter{seen: make(chan context.Context, 4)}, &doorAudit{}
			exec, err := conductor.NewExecutor(conductor.ExecutorConfig{Router: router, Resolver: &doorProvider{}, Audit: aw,
				Clock: runtime.NewSystemClock(), Classifier: sec.Classifier, Taxonomy: sec.Taxonomy, Policy: sec.Policy,
				Sensitivity: sec.Sensitivity, Firewall: sec.Firewall})
			if err != nil {
				t.Fatal(err)
			}
			d := &ConductorDoor{}
			d.bind(exec, doorLanes{}, aw, nil)
			who := &principal.Principal{ID: "p-1", Kind: principal.Kind("session")}
			ctx, cancel := context.WithCancel(principal.WithCaller(context.WithValue(context.Background(), doorCtxKey{}, "v"), principal.Caller{Principal: who}))
			defer cancel()
			call(d, ctx)
			got := <-router.seen
			if c, ok := principal.CallerFrom(got); !ok || c.Principal == nil || c.Principal.ID != who.ID {
				t.Errorf("Select saw caller %v (ok=%v), want the caller's principal", c, ok)
			}
			if got.Value(doorCtxKey{}) != "v" {
				t.Error("Select did not see the caller's ctx value")
			}
			cancel()
			if got.Err() == nil {
				t.Error("cancelling the caller ctx did not cancel the ctx Select received")
			}
		})
	}
}
