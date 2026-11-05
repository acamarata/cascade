// Purpose: the agent-lane half of the domain promote probe (P1-TOP-19d):
//
//	a subscription harness lane yields one capabilities evidence row; an
//	entitlement error, a disabled plugin, an absent binary or a posture
//	without programmatic entitlement yields none.
//
// Inputs: the real harness resolver and claude driver factory over a
// temporary regular file standing in for the vendor binary.
//
// Outputs: none.
// Constraints: no network; no child process is started.
//
// SPORT: cmd/cascade domain_promote_probe/ADD (P1-TOP-19d).
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// entitlementDriver is a harness driver whose Capabilities refuses.
type entitlementDriver struct {
	provider.AgentProvider
	exe string
}

func (d entitlementDriver) Executable() string { return d.exe }

func (entitlementDriver) Capabilities(context.Context, string) (provider.Capabilities, error) {
	return provider.Capabilities{}, provider.ErrEntitlement
}

// subDomain is the subscription quota domain under test.
const subDomain = "claude:subscription"

// fakeLanes resolves every lane to claude's binding and a fixed factory.
type fakeLanes struct{ factory agents.DriverFactory }

func (f fakeLanes) Resolve(string) (agents.Binding, agents.DriverFactory, error) {
	for _, b := range agents.Bindings() {
		if b.Harness == agents.HarnessClaude {
			return b, f.factory, nil
		}
	}
	return agents.Binding{}, nil, errors.New("claude binding not registered")
}

// agentProbe builds a probe over the real resolver with the given knobs;
// the harness's grant class is the part of the domain after "claude:".
func agentProbe(t *testing.T, enabled, entitled bool, bin string) *promoteProbe {
	t.Helper()
	res, err := agents.NewResolver(func(string) bool { return enabled })
	if err != nil {
		t.Fatal(err)
	}
	h, class := agents.HarnessClaude, strings.TrimPrefix(subDomain, "claude:")
	g := make(map[agents.HarnessID]agents.DriverAuth)
	g[h] = agents.DriverAuth{Harness: h, Class: class}
	posture := provider.NewCompliancePosture([]string{class}, true, entitled, nil, "", false)
	shim := agents.ShimDeps{Resolver: res, Auth: g,
		Binaries: map[agents.HarnessID]string{h: bin},
		Postures: map[agents.HarnessID]provider.CompliancePosture{h: posture},
		Defaults: map[agents.HarnessID]provider.DriverDefaults{h: {CancelGrace: time.Second}},
	}
	return &promoteProbe{lanes: res, shim: shim, clock: runtime.NewSystemClock(), by: "operator"}
}

// fakeBinary writes a regular file standing in for the vendor binary.
func fakeBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestPromoteAgentSubscriptionDomain(t *testing.T) {
	ctx, domain := context.Background(), topology.DomainID(subDomain)
	lane := agents.LanePrefix + string(agents.HarnessClaude)
	key := topology.EvidenceKey{Provider: lane}
	bin := fakeBinary(t)

	ev, err := agentProbe(t, true, true, bin).probeAgent(ctx, domain, key)
	if err != nil {
		t.Fatalf("subscription lane probe: %v", err)
	}
	if ev.ProbeKind != promoteKindCapabilities || ev.ProbeStatus != 0 || ev.DomainID != domain ||
		ev.TestOutcome != topology.EvidenceOutcomePassed || ev.Key != key || ev.ObservedAt.IsZero() {
		t.Fatalf("evidence = %+v, want one passed capabilities row", ev)
	}

	refusing := agentProbe(t, true, true, bin)
	refusing.lanes = fakeLanes{factory: func(agents.SpawnSite) (provider.AgentProvider, error) {
		return entitlementDriver{exe: bin}, nil
	}}
	cases := []struct {
		name, check string
		probe       *promoteProbe
	}{
		{"ErrEntitlement", "capabilities", refusing},
		{"plugin disabled", "plugin", agentProbe(t, false, true, bin)},
		{"binary absent", "binary", agentProbe(t, true, true, filepath.Join(t.TempDir(), "absent"))},
		{"entitlement false", "entitlement", agentProbe(t, true, false, bin)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := tc.probe.probeAgent(ctx, domain, key)
			if err == nil {
				t.Fatalf("probe passed with %+v, want a refusal", ev)
			}
			if k, _ := cascade.KindOf(err); k != cascade.KindUnavailable {
				t.Fatalf("kind = %v, want %v (%v)", k, cascade.KindUnavailable, err)
			}
			if msg := err.Error(); !strings.Contains(msg, lane) || !strings.Contains(msg, `"`+tc.check+`"`) {
				t.Fatalf("error %q does not name lane %q and check %q", msg, lane, tc.check)
			}
			if ev != (topology.BillingEvidence{}) {
				t.Fatalf("refusal carried evidence %+v", ev)
			}
		})
	}
}
