package host

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakePolicyEngine returns a fixed verdict/explanation/error, and records
// the pluginID/capability pair it was asked about.
type fakePolicyEngine struct {
	verdict       PolicyVerdict
	explanation   string
	err           error
	gotPluginID   string
	gotCapability string
}

func (p *fakePolicyEngine) Evaluate(_ context.Context, pluginID, capability string) (PolicyVerdict, string, error) {
	p.gotPluginID = pluginID
	p.gotCapability = capability
	return p.verdict, p.explanation, p.err
}

func TestCheckGenericAllow(t *testing.T) {
	sink := &fakeAuditSink{}
	engine := &fakePolicyEngine{verdict: PolicyVerdictAllow}
	e, err := NewHostBoundaryEnforcer("plugin-a", Grants{}, engine, nil, sink)
	if err != nil {
		t.Fatalf("build enforcer: %v", err)
	}
	if err := e.CheckGeneric(context.Background(), "host.tool_register"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if engine.gotPluginID != "plugin-a" || engine.gotCapability != "host.tool_register" {
		t.Fatalf("engine asked about (%q, %q), want (plugin-a, host.tool_register)", engine.gotPluginID, engine.gotCapability)
	}
	if len(sink.events) != 0 {
		t.Fatalf("an allowed CheckGeneric call must not audit, got %+v", sink.events)
	}
}

func TestCheckGenericDenyCarriesEngineExplanation(t *testing.T) {
	sink := &fakeAuditSink{}
	engine := &fakePolicyEngine{verdict: PolicyVerdictDeny, explanation: "layer 2 (elevation): DECIDED deny, because capability requires elevation"}
	e, _ := NewHostBoundaryEnforcer("plugin-a", Grants{}, engine, nil, sink)

	err := e.CheckGeneric(context.Background(), "host.tool_register")
	if !errors.Is(err, cascade.ErrCapabilityDenied) {
		t.Fatalf("error = %v, want KindCapabilityDenied", err)
	}
	if !strings.Contains(err.Error(), engine.explanation) {
		t.Fatalf("denial error %q does not carry the engine's explanation %q", err.Error(), engine.explanation)
	}
	entry, ok := sink.last()
	if !ok || entry.Allowed || !strings.Contains(entry.Reason, engine.explanation) {
		t.Fatalf("audit entry = %+v (ok=%v), want a denied entry carrying the explanation", entry, ok)
	}
}

func TestCheckGenericAskIsTreatedAsDeny(t *testing.T) {
	sink := &fakeAuditSink{}
	engine := &fakePolicyEngine{verdict: PolicyVerdictAsk, explanation: "would ask an operator"}
	e, _ := NewHostBoundaryEnforcer("plugin-a", Grants{}, engine, nil, sink)

	err := e.CheckGeneric(context.Background(), "host.tool_register")
	if !errors.Is(err, cascade.ErrCapabilityDenied) {
		t.Fatalf("an ask verdict at the host boundary must deny; got %v", err)
	}
}

func TestCheckGenericFailClosedCases(t *testing.T) {
	t.Run("empty capability denies without calling the engine", func(t *testing.T) {
		sink := &fakeAuditSink{}
		engine := &fakePolicyEngine{verdict: PolicyVerdictAllow}
		e, _ := NewHostBoundaryEnforcer("plugin-a", Grants{}, engine, nil, sink)
		if err := e.CheckGeneric(context.Background(), ""); !errors.Is(err, cascade.ErrCapabilityDenied) {
			t.Fatalf("error = %v, want KindCapabilityDenied", err)
		}
		if engine.gotCapability != "" {
			t.Fatal("the engine must never be called for an empty capability")
		}
	})

	t.Run("nil engine denies", func(t *testing.T) {
		sink := &fakeAuditSink{}
		e, _ := NewHostBoundaryEnforcer("plugin-a", Grants{}, nil, nil, sink)
		if err := e.CheckGeneric(context.Background(), "host.tool_register"); !errors.Is(err, cascade.ErrCapabilityDenied) {
			t.Fatalf("error = %v, want KindCapabilityDenied", err)
		}
	})

	t.Run("evaluation error denies", func(t *testing.T) {
		sink := &fakeAuditSink{}
		engine := &fakePolicyEngine{verdict: PolicyVerdictAllow, err: cascade.New(cascade.KindUnavailable, "engine unreachable")}
		e, _ := NewHostBoundaryEnforcer("plugin-a", Grants{}, engine, nil, sink)
		if err := e.CheckGeneric(context.Background(), "host.tool_register"); !errors.Is(err, cascade.ErrCapabilityDenied) {
			t.Fatalf("error = %v, want KindCapabilityDenied", err)
		}
	})

	t.Run("zero-value verdict (unset field) denies", func(t *testing.T) {
		sink := &fakeAuditSink{}
		var engine fakePolicyEngine // verdict defaults to PolicyVerdictDeny (zero value)
		e, _ := NewHostBoundaryEnforcer("plugin-a", Grants{}, &engine, nil, sink)
		if err := e.CheckGeneric(context.Background(), "host.tool_register"); !errors.Is(err, cascade.ErrCapabilityDenied) {
			t.Fatalf("error = %v, want KindCapabilityDenied", err)
		}
	})
}

func TestPolicyVerdictZeroValueIsDeny(t *testing.T) {
	var v PolicyVerdict
	if v != PolicyVerdictDeny {
		t.Fatalf("PolicyVerdict zero value = %v, want PolicyVerdictDeny", v)
	}
}
