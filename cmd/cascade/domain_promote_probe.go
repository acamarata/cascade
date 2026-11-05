// Purpose: the domain promote probe (P1-TOP-19): before a quota domain is
//
//	promoted to paid, every provider whose credentials reference it is
//	proved usable by one authenticated, 1-token Chat through the daemon's
//	one dispatch resolver, and every agent harness lane by its resolved
//	driver's declared capabilities. Each pass yields one passed
//	topology.BillingEvidence row; anything else is KindUnavailable naming
//	the provider and the failed check, with no evidence.
//
// Inputs: the registry read view and the daemon's resolver (registry
//
//	providers); the agent harness resolver and the shim's dependencies
//	(agent lanes); an injected clock.
//
// Outputs: BillingEvidence values. Nothing is written here (P1-TOP-19e
//
//	writes them through topology.Store.PromoteDomain).
//
// Constraints: the probe call runs under dispatch.WithoutTransientCount
//
//	and a promoteProbeTimeout deadline, so it neither demotes nor rescues
//	the provider; the reachability prober is never used as evidence; no
//	second resolver, credential source or client; no child process.
//
// SPORT: cmd/cascade domain_promote_probe/ADD (P1-TOP-19d).
package main

import (
	"context"
	"os"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/providers/agents"
	providerdispatch "github.com/acamarata/cascade/internal/providers/dispatch"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// promoteProbeTimeout bounds one probe call; overridden only in tests.
var promoteProbeTimeout = 30 * time.Second

// promotePing is the constant, public probe prompt.
const promotePing = "ping"

// Probe kinds recorded on the evidence row.
const (
	promoteKindChat         = "chat"
	promoteKindCapabilities = "capabilities"
)

// promoteLookup is the registry read view the probe needs.
type promoteLookup interface {
	ListLanes(ctx context.Context) ([]providerregistry.LaneRecord, error)
	GetProvider(ctx context.Context, name string) (providerregistry.ProviderRecord, error)
}

// promoteResolver is the daemon's one provider resolver.
type promoteResolver interface {
	Resolve(ctx context.Context, sel provider.Selection) (provider.ModelProvider, error)
}

// agentLanes resolves a harness lane; *agents.Resolver satisfies it.
type agentLanes interface {
	Resolve(lane string) (agents.Binding, agents.DriverFactory, error)
}

// promoteProbe runs the promote checks. Build it from the daemon's own
// lookup, resolver and shim dependencies.
type promoteProbe struct {
	lookup  promoteLookup
	resolve promoteResolver
	lanes   agentLanes
	shim    agents.ShimDeps
	clock   runtime.Clock
	by      string
}

// promoteRefusal is the KindUnavailable naming the provider and check.
func promoteRefusal(name, check string, cause error) error {
	if cause == nil {
		return cascade.Newf(cascade.KindUnavailable, "promote probe: provider %q failed check %q", name, check)
	}
	return cascade.Wrapf(cascade.KindUnavailable, cause, "promote probe: provider %q failed check %q", name, check)
}

// passed builds one passed evidence row.
func (p *promoteProbe) passed(domain topology.DomainID, key topology.EvidenceKey, kind string, status int, endpoint string) topology.BillingEvidence {
	return topology.BillingEvidence{DomainID: domain, Key: key, PromotedBy: p.by,
		TestOutcome: topology.EvidenceOutcomePassed, ProbeKind: kind, ProbeStatus: status,
		ProbeEndpoint: endpoint, ObservedAt: p.clock.Now()}
}

// probeRegistry proves key.Provider with one authenticated 1-token Chat.
func (p *promoteProbe) probeRegistry(ctx context.Context, domain topology.DomainID, key topology.EvidenceKey) (topology.BillingEvidence, error) {
	name := key.Provider
	rec, sel, err := p.selection(ctx, name)
	if err != nil {
		return topology.BillingEvidence{}, err
	}
	ctx, cancel := context.WithTimeout(providerdispatch.WithoutTransientCount(ctx), promoteProbeTimeout)
	defer cancel()
	mp, err := p.resolve.Resolve(ctx, sel)
	if err != nil {
		return topology.BillingEvidence{}, promoteRefusal(name, "resolve", err)
	}
	resp, err := mp.Chat(ctx, provider.ChatRequest{Model: sel.Model, MaxOutputTokens: 1,
		Messages: []provider.ChatMessage{{Role: "user", Content: promotePing}}})
	if err != nil {
		return topology.BillingEvidence{}, promoteRefusal(name, "chat", err)
	}
	if resp.Message.Role != "assistant" || (resp.Message.Content == "" && resp.Usage.OutputTokens < 1) {
		return topology.BillingEvidence{}, promoteRefusal(name, "reply", nil)
	}
	return p.passed(domain, key, promoteKindChat, 200, rec.BaseURL), nil
}

// selection picks the provider's first registry lane and its model.
func (p *promoteProbe) selection(ctx context.Context, name string) (providerregistry.ProviderRecord, provider.Selection, error) {
	rec, err := p.lookup.GetProvider(ctx, name)
	if err != nil {
		return rec, provider.Selection{}, promoteRefusal(name, "provider", err)
	}
	if rec.Auth == providerregistry.AuthOAuth {
		return rec, provider.Selection{}, promoteRefusal(name, "oauth", nil)
	}
	lanes, err := p.lookup.ListLanes(ctx)
	if err != nil {
		return rec, provider.Selection{}, promoteRefusal(name, "lane", err)
	}
	for _, lane := range lanes {
		if lane.ProviderName != name {
			continue
		}
		sel := provider.Selection{LaneID: lane.LaneName, Provider: string(rec.Driver)}
		switch {
		case len(lane.ModelFilter) > 0:
			sel.Model = lane.ModelFilter[0]
		case len(rec.KnownModels) > 0:
			sel.Model = rec.KnownModels[0]
		default:
			return rec, provider.Selection{}, promoteRefusal(name, "no model", nil)
		}
		return rec, sel, nil
	}
	return rec, provider.Selection{}, promoteRefusal(name, "no lane", nil)
}

// probeAgent proves an agent harness lane (key.Provider is the lane) from
// its resolved driver's declared capabilities; no child process runs.
func (p *promoteProbe) probeAgent(ctx context.Context, domain topology.DomainID, key topology.EvidenceKey) (topology.BillingEvidence, error) {
	lane := key.Provider
	if p.lanes == nil {
		return topology.BillingEvidence{}, promoteRefusal(lane, "plugin", nil)
	}
	b, factory, err := p.lanes.Resolve(lane)
	if err != nil {
		return topology.BillingEvidence{}, promoteRefusal(lane, "plugin", err)
	}
	h := b.Harness
	if _, ok := p.shim.Auth[h]; !ok {
		return topology.BillingEvidence{}, promoteRefusal(lane, "auth", nil)
	}
	// A probe-only site: no clock, no credential variable, no worktree.
	drv, err := factory(agents.SpawnSite{Binary: p.shim.Binaries[h], Posture: p.shim.Postures[h], Defaults: p.shim.Defaults[h]})
	if err != nil {
		return topology.BillingEvidence{}, promoteRefusal(lane, "binary", err)
	}
	rep, ok := drv.(provider.ExecutableReporter)
	if !ok {
		return topology.BillingEvidence{}, promoteRefusal(lane, "binary", nil)
	}
	if fi, err := os.Stat(rep.Executable()); err != nil || !fi.Mode().IsRegular() {
		return topology.BillingEvidence{}, promoteRefusal(lane, "binary", err)
	}
	caps, err := drv.Capabilities(ctx, lane)
	if err != nil {
		return topology.BillingEvidence{}, promoteRefusal(lane, "capabilities", err)
	}
	if !caps.CompliancePosture.ProgrammaticEntitlement {
		return topology.BillingEvidence{}, promoteRefusal(lane, "entitlement", nil)
	}
	return p.passed(domain, key, promoteKindCapabilities, 0, ""), nil
}
