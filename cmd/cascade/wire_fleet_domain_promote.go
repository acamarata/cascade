//go:build !windows

// Purpose: owner-only fleet.domains.promote (elevated), demote, forget: probe and
// key each provider in the daemon, intent, one tx, confirm; demotes prune.
// Inputs: the topology store; the conductor's registry, resolver, credentials.
// Constraints: requests carry a target and tier only; a failed probe or key
// writes nothing; a prune error keeps rows and is returned as prune_error.
// SPORT: cmd/cascade daemon registrations.

package main

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	promoteVerb                 = "fleet.domains.promote"
	demoteVerb                  = "fleet.domains.demote"
	forgetVerb                  = "fleet.domains.forget"
	fleetDomainPromoteSubsystem = "fleet-domain-promote"
	topologyReconciledKind      = events.EventKind("fleet.topology.reconciled")
)

var (
	_ = registerDaemonWiring(daemonRegistration{Name: fleetDomainPromoteSubsystem, Phase: phaseFleet, Order: 65, Wire: wireFleetDomainPromote})
	_ = rpc.DeclareAccess(promoteVerb, rpc.AccessOwnerOnly)
	_ = rpc.DeclareAccess(demoteVerb, rpc.AccessOwnerOnly)
	_ = rpc.DeclareAccess(forgetVerb, rpc.AccessOwnerOnly)
)

func init() { rpc.RegisterElevatedVerb(promoteVerb, rpc.ElevationRule{Always: true}) }

// afterPromoteIntent is a test seam between the intent and the tx (a crash).
var afterPromoteIntent = func() error { return nil }

var promoteHandoff atomic.Pointer[domainPromoter] // the conductor's probe and key inputs, one daemon per process

// domainPromoteParts builds the promoter (nil, nil: no fleet runtime); a seam.
var domainPromoteParts = func(w *daemonWiring) (*domainPromoter, error) {
	if w.Deps.Fleet == nil || w.Paths == nil || w.Store == nil {
		return nil, nil
	}
	v := promoteHandoff.Load()
	if v == nil || w.Deps.Fleet.Topology() == nil {
		return nil, cascade.New(cascade.KindUnavailable, "fleet domain promote: no topology store or provider resolver")
	}
	d := *v
	d.topo, d.logger, d.keys.KeyPath = w.Deps.Fleet.Topology(), cmp.Or(w.Logger, slog.Default()), filepath.Join(w.Paths.DataDir(), "billing-evidence.key")
	var err error
	if d.key, err = newEvidenceKeyFn(d.keys); err != nil {
		return nil, err
	}
	if _, err = openDaemonAuditLog(w.Store, w.Clock, nil, w.Paths); err != nil {
		return nil, err
	}
	d.effects, err = openEffectLog(w.Store, w.Clock, nil, w.Paths)
	return &d, err
}

// wireFleetDomainPromote settles open intents, registers verbs, starts the prune.
func wireFleetDomainPromote(w *daemonWiring) error {
	d, err := domainPromoteParts(w)
	if d == nil && err == nil {
		return nil
	}
	var pending []audit.EffectHandle // intents a crash left open: unknown-outcome
	if err == nil {
		pending, err = d.effects.PendingEffects(w.Ctx)
	}
	for i := 0; err == nil && i < len(pending); i++ {
		if strings.HasPrefix(pending[i].Key, promoteVerb+":") {
			_, err = d.effects.MarkUnknownOutcome(w.Ctx, pending[i])
		}
	}
	var sub *events.Subscription
	if err == nil {
		sub, err = w.Bus.SubscribeFromHead(w.Ctx, "fleet.topology", fleetDomainPromoteSubsystem, 16)
	}
	if err != nil {
		w.Manifest.Failed(fleetDomainPromoteSubsystem, err.Error())
		return nil
	}
	w.Registry.Register(promoteVerb, func(ctx context.Context, raw json.RawMessage) (any, error) {
		witness, ok := rpc.VerifiedElevationFrom(ctx)
		if !ok || witness.Method() != promoteVerb {
			return nil, cascade.New(cascade.KindElevationRequired, promoteVerb+" requires local elevation")
		}
		return d.promote(ctx, raw, witness.ParamsHash())
	})
	w.Registry.Register(demoteVerb, d.demote)
	w.Registry.Register(forgetVerb, d.forget)
	w.Manifest.GoSupervised(w.Ctx, fleetDomainPromoteSubsystem+"-prune", "on every reconciled pass", func(ctx context.Context) error {
		defer context.AfterFunc(ctx, func() { _ = sub.Unsubscribe() })()
		for ev := range sub.Events { // closed by Unsubscribe or a bus stop
			if ev.Kind == topologyReconciledKind {
				d.prune(ctx)
			}
		}
		if ctx.Err() == nil {
			return cascade.New(cascade.KindUnavailable, "fleet domain promote: the topology subscription stopped")
		}
		return nil
	})
	w.Manifest.Started(fleetDomainPromoteSubsystem, "promote, demote, forget")
	return nil
}

// domainPromoter is the core of the three verbs and the prune.
type domainPromoter struct {
	topo    *topology.Store
	probe   promoteProbe
	keys    evidenceKeyDeps
	key     topology.EvidenceKeyFn
	effects *audit.EffectLog
	logger  *slog.Logger
}

// domainTarget is the only request shape (keys domain, provider, tier).
type domainTarget struct{ Domain, Provider, Tier string }

// promote probes and keys every provider of the target, then writes.
func (d *domainPromoter) promote(ctx context.Context, raw json.RawMessage, paramsHash string) (any, error) {
	p, err := parseTarget(raw, promoteVerb, string(topology.BillingTierPaid), false)
	if err != nil {
		return nil, err
	}
	// PromotedBy is the transport caller (owner-only: no principal gets here).
	probe := d.probe
	c, _ := principal.CallerFrom(ctx)
	probe.by = map[bool]string{true: "owner-cli", false: "owner"}[c.OwnerCLI]
	domain, providers, err := d.resolveTarget(ctx, p)
	if err != nil {
		return nil, err
	}
	ev := make([]topology.BillingEvidence, 0, len(providers))
	for _, name := range providers {
		key, err := d.key(ctx, topology.AccountID(name))
		if err != nil {
			return nil, promoteRefusal(name, "key", err)
		}
		run := probe.probeRegistry
		if strings.HasPrefix(name, agents.LanePrefix) {
			run = probe.probeAgent
		}
		row, err := run(ctx, domain, key)
		if err != nil {
			return nil, err
		}
		ev = append(ev, row)
	}
	err = d.commit(ctx, domain, providers, ev, probe.by, paramsHash)
	return map[string]any{"domain": domain, "tier": p.Tier, "providers": providers, "evidence": len(ev)}, err
}

// commit records the intent, runs the one promotion tx, then confirms.
func (d *domainPromoter) commit(ctx context.Context, domain topology.DomainID, providers []string, ev []topology.BillingEvidence, by, paramsHash string) error {
	explain, _ := json.Marshal(map[string]any{"domain": domain, "providers": providers}) // strings only
	key := promoteVerb + ":" + audit.HashParams([]byte(domain))[:16] + ":" + strconv.FormatInt(d.probe.clock.Now().UnixNano(), 10)
	h, err := d.effects.BeginEffect(ctx, audit.EffectRequest{Kind: audit.KindExternalEffect, Actor: by,
		Action: promoteVerb, Key: key, ParamsHash: paramsHash, Explain: explain})
	if err == nil {
		err = afterPromoteIntent()
	}
	if err != nil {
		return err
	}
	terminal := context.WithoutCancel(ctx)
	if err := d.topo.PromoteDomain(ctx, domain, ev); err != nil {
		_, _ = d.effects.FailEffect(terminal, h, "the promotion transaction rolled back")
		return err
	}
	_, _ = d.effects.ConfirmEffect(terminal, h, string(topology.BillingTierPaid)) // open intent: recovery marks unknown
	return nil
}

// demote sets the target to discover and prunes; a prune error is reported.
func (d *domainPromoter) demote(ctx context.Context, raw json.RawMessage) (any, error) {
	p, err := parseTarget(raw, demoteVerb, string(topology.BillingTierDiscover), true)
	if err != nil {
		return nil, err
	}
	domain, _, err := d.resolveTarget(ctx, p)
	if err == nil {
		err = d.topo.DemoteDomain(ctx, domain)
	}
	if err != nil {
		return nil, err
	}
	n, perr := d.prune(ctx)
	return map[string]any{"domain": domain, "tier": topology.BillingTierDiscover, "pruned": n, "prune_error": perr}, nil
}

// forget deletes one provider's evidence rows.
func (d *domainPromoter) forget(ctx context.Context, raw json.RawMessage) (any, error) {
	p, err := parseTarget(raw, forgetVerb, "", true)
	if err != nil {
		return nil, err
	}
	n, err := d.topo.ForgetProviderEvidence(ctx, p.Provider)
	return map[string]any{"provider": p.Provider, "forgotten": n}, err
}

// parseTarget decodes raw; tier must be want (or empty when optional).
func parseTarget(raw json.RawMessage, verb, want string, optional bool) (domainTarget, error) {
	var p domainTarget
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, cascade.Wrap(cascade.KindInvalidInput, err, verb+" parameters")
	}
	if p.Tier != want && (!optional || p.Tier != "") {
		return p, cascade.Newf(cascade.KindInvalidInput, "%s: tier must be %q", verb, want)
	}
	return p, nil
}

// prune drops stale evidence, then demotes every paid domain a provider of
// which lacks its own row; an error keeps every row and is one log line.
func (d *domainPromoter) prune(ctx context.Context) (int, string) {
	n, err := d.topo.PruneBillingEvidence(ctx, d.key)
	doms, lerr := d.topo.ListQuotaDomains(ctx)
	err = cmp.Or(err, lerr)
	for i := 0; err == nil && i < len(doms); i++ {
		if doms[i].BillingTier == topology.BillingTierPaid {
			err = d.demoteUncovered(ctx, doms[i].ID)
		}
	}
	if err == nil {
		return n, ""
	}
	d.logger.Warn("fleet domain evidence prune failed", slog.String("error", err.Error()))
	return 0, err.Error()
}

// resolveTarget names one domain and every provider whose credentials
// reference it. A provider spanning several domains is KindConflict.
func (d *domainPromoter) resolveTarget(ctx context.Context, p domainTarget) (topology.DomainID, []string, error) {
	domain, prov := strings.TrimSpace(p.Domain), strings.TrimSpace(p.Provider)
	if (domain == "") == (prov == "") {
		return "", nil, cascade.New(cascade.KindInvalidInput, "fleet domains: name exactly one of domain or provider")
	}
	creds, err := d.topo.ListCredentials(ctx)
	if err != nil {
		return "", nil, err
	}
	if prov != "" {
		owned := map[string]bool{}
		for _, c := range creds {
			if string(c.AccountRef) == prov {
				owned[string(c.QuotaDomainRef)] = true
			}
		}
		ids := slices.Sorted(maps.Keys(owned))
		if len(ids) != 1 { // none is KindNotFound; several, KindConflict
			kind := map[bool]cascade.Kind{true: cascade.KindNotFound, false: cascade.KindConflict}[len(ids) == 0]
			return "", nil, cascade.Newf(kind, "fleet domains: provider %q has quota domains %v; name one domain", prov, ids)
		}
		domain = ids[0]
	}
	q, err := d.topo.GetQuotaDomain(ctx, topology.DomainID(domain))
	if err != nil {
		return "", nil, err
	}
	providers := map[string]bool{string(q.AccountRef): true}
	for _, c := range creds {
		if c.QuotaDomainRef == q.ID && c.AccountRef != "" {
			providers[string(c.AccountRef)] = true
		}
	}
	return q.ID, slices.Sorted(maps.Keys(providers)), nil
}

// demoteUncovered demotes id if a provider lacks a row (prune keys by account).
func (d *domainPromoter) demoteUncovered(ctx context.Context, id topology.DomainID) error {
	_, provs, err := d.resolveTarget(ctx, domainTarget{Domain: string(id)})
	ev, eerr := d.topo.BillingEvidenceFor(ctx, id)
	err = cmp.Or(err, eerr)
	for _, e := range ev {
		provs = slices.DeleteFunc(provs, func(p string) bool { return p == e.Key.Provider })
	}
	if err != nil || len(provs) == 0 {
		return err
	}
	return d.topo.DemoteDomain(ctx, id)
}
