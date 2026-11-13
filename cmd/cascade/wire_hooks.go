//go:build !windows

// Purpose: registers the hooks daemon wiring: the ONE hooks.Dispatcher over
// the admitted [hooks] section, its reload watch, its attention feed and the
// hooks.list / hooks.fires verbs.
// Inputs: w.Deps.Reloader (the admitted config), w.Deps.Policy (router and
// capability registry), the process egress firewall, the effect log and the
// runner table (hooks_runners.go).
// Outputs: a running Dispatcher; a Failed manifest entry and verbs that
// answer "hooks subsystem degraded" when any part cannot be built.
// Constraints: one Dispatcher, one rehydration seam, no shell runner. Every
// action routes through the policy Router as the daemon service subject. A
// hook edit that was not accepted is never applied and raises one attention
// item per edit. Startup recovery marks open hook effects unknown, never
// re-runs them. A build error never stops the daemon.
// SPORT: cmd/cascade hooks composition (P1-SUP-02).
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/hooks"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The hooks manifest entries and the capabilities each action type is routed as.
const (
	hooksSubsystem          = "hooks"
	hooksReloadSubsystem    = "hooks-reload"
	hooksAttentionSubsystem = "hooks-attention"
	hooksPluginCallCap      = "hooks.plugin_call"
	hooksAgentNoteCap       = "hooks.agent_note"
	hooksReloadCursor       = "hooks-reload"
	hooksAttentionCursor    = "hooks-attention"
	hooksActionTimeout      = 10 * time.Second
	hooksSubscribeBuffer    = 64
)

var _ = registerDaemonWiring(daemonRegistration{Name: hooksSubsystem, Phase: phaseSupervised, Order: 40, Wire: wireHooks})

// hooksParts is everything buildHooks constructs before anything starts.
type hooksParts struct {
	d         *hooks.Dispatcher
	watch     *hooks.ReloadWatch
	rejects   *events.Subscription
	attention *supervision.Store
}

func wireHooks(w *daemonWiring) error { return wireHooksWith(w, buildHooks) }

// wireHooksWith builds and starts the hooks parts. Any build error is
// recorded Failed, both verbs answer degraded, and the daemon boots with
// zero hooks firing.
func wireHooksWith(w *daemonWiring, build func(*daemonWiring) (*hooksParts, error)) error {
	parts, err := build(w)
	if err != nil {
		w.Manifest.Failed(hooksSubsystem, err.Error())
		registerHooksRPC(w.Registry, nil, nil)
		return nil
	}
	registerHooksRPC(w.Registry, parts.d, nil)
	w.Manifest.GoSupervised(w.Ctx, hooksSubsystem, "dispatching configured hooks", parts.d.Run)
	w.Manifest.GoSupervised(w.Ctx, hooksReloadSubsystem, "applying accepted [hooks] reloads", func(ctx context.Context) error {
		return parts.watch.Run(ctx, hooksSectionSource(w.Deps.Reloader), hooksParser, parts.d)
	})
	w.Manifest.GoSupervised(w.Ctx, hooksAttentionSubsystem, "filing refused hook edits", func(ctx context.Context) error {
		return watchRefusedHookEdits(ctx, w, parts)
	})
	return nil
}

// hooksParser reads the canonical hook list the family extractor produced.
func hooksParser(raw any) ([]hooks.HookConfig, error) {
	v, _ := raw.(json.RawMessage)
	return decodeHookList(v)
}

// hooksSectionSource reads the canonical [hooks] value (the family extractor,
// which also validates it) of the reloader's current config.
func hooksSectionSource(hr *runtime.HotReloader) func() (any, error) {
	return func() (any, error) {
		if hr == nil || hr.Current() == nil {
			return nil, cascade.New(cascade.KindUnavailable, "hooks: no admitted configuration")
		}
		return hooksFamilyExtract(hr.Current().Extra)
	}
}

// hooksSeams are the process-wide pieces the Dispatcher is built over.
type hooksSeams struct {
	engine     *egress.Engine
	token      egress.Capability
	rehydrator *secrets.Rehydrator
}

// hooksSeamsFor builds the egress engine, token and rehydrator.
func hooksSeamsFor(w *daemonWiring) (hooksSeams, error) {
	fw, err := processFirewallFor(w.Paths)
	if err != nil {
		return hooksSeams{}, err
	}
	engine, err := fw.Engine(egress.DefaultRegistry())
	if err != nil {
		return hooksSeams{}, err
	}
	token, err := egress.DefaultRegistry().Capability(egress.EgressClassHook)
	if err != nil {
		return hooksSeams{}, err
	}
	rehydrator, err := secrets.NewRehydrator(fw.Broker())
	return hooksSeams{engine: engine, token: token, rehydrator: rehydrator}, err
}

// buildHooks constructs the Dispatcher and everything it needs. The reload
// watch is subscribed before the initial registry is read, so an accepted
// reload between the two is never missed.
func buildHooks(w *daemonWiring) (*hooksParts, error) {
	if w.Deps.Reloader == nil || w.Deps.Policy == nil {
		return nil, cascade.New(cascade.KindUnavailable, "hooks: no config reloader or policy wiring")
	}
	seams, err := hooksSeamsFor(w)
	if err != nil {
		return nil, err
	}
	runners, err := hookRunnersFor(w)
	if err != nil {
		return nil, err
	}
	for _, c := range []string{hooksPluginCallCap, hooksAgentNoteCap} {
		if err := registerHookCapability(w.Ctx, w.Deps.Policy.Registry, c); err != nil {
			return nil, err
		}
	}
	watch, err := hooks.WatchReload(w.Ctx, w.Bus, hotReloadEventNamespace, reloadAcceptedEvent, hooksReloadCursor)
	if err != nil {
		return nil, err
	}
	reg, err := initialHooksRegistry(w.Deps.Reloader)
	if err != nil {
		return nil, err
	}
	d, err := hooks.NewDispatcher(hooks.DispatcherConfig{
		Registry: reg, Bus: w.Bus, Clock: w.Clock, Egress: seams.engine, EgressToken: seams.token, Runners: runners,
		ActionCapabilities: map[hooks.ActionType]string{hooks.ActionTypePluginCall: hooksPluginCallCap, hooks.ActionTypeAgentNote: hooksAgentNoteCap},
		Rehydrate:          hooks.BoundRehydrate(admittedHookBindings(w.Deps.Reloader), rehydrateOver(seams.rehydrator)),
		Router:             w.Deps.Policy.Router, RouteSubject: schedulerSubject(), State: w.Store,
		ActionTimeout: hooksActionTimeout, AuditNamespace: hooks.AuditNamespace, CursorPrefix: "hooks:", SubscribeBuffer: hooksSubscribeBuffer,
	})
	if err != nil {
		return nil, err
	}
	return hooksAttentionParts(w, d, watch)
}

// hooksAttentionParts recovers open hook effects, subscribes the rejection
// feed and files the boot-time attention item for an unaccepted edit.
func hooksAttentionParts(w *daemonWiring, d *hooks.Dispatcher, watch *hooks.ReloadWatch) (*hooksParts, error) {
	effects, err := openEffectLog(w.Store, w.Clock, nil, w.Paths)
	if err != nil {
		return nil, err
	}
	if _, err := recoverHookEffects(w.Ctx, effects); err != nil {
		return nil, err
	}
	sub, err := w.Bus.SubscribeFromHead(w.Ctx, hotReloadEventNamespace, hooksAttentionCursor, hooksSubscribeBuffer)
	if err != nil {
		return nil, err
	}
	parts := &hooksParts{d: d, watch: watch, rejects: sub,
		attention: supervision.NewStore(w.Store, w.Clock, w.Bus, supervision.NewSystemIDGenerator(), 0)}
	raiseUnacceptedHookEdit(w.Ctx, w, parts.attention)
	return parts, nil
}

// initialHooksRegistry registers the admitted hooks of the reloader's config.
func initialHooksRegistry(hr *runtime.HotReloader) (*hooks.Registry, error) {
	raw, err := hooksSectionSource(hr)()
	if err != nil {
		return nil, err
	}
	list, err := hooksParser(raw)
	if err != nil {
		return nil, err
	}
	reg := hooks.NewRegistry()
	for _, cfg := range list {
		if _, err := reg.Register(cfg); err != nil {
			return nil, err
		}
	}
	return reg, nil
}

// registerHookCapability adds name (ClassLocalDev) to the daemon's one
// capability registry; an identical existing entry is accepted.
func registerHookCapability(ctx context.Context, reg policy.CapabilityRegistry, name string) error {
	want := policy.Capability{Name: name, Desc: "run a configured hook action", DefaultPolicy: policy.ClassLocalDev}
	err := reg.Add(ctx, want)
	if err == nil || !cascade.HasKind(err, cascade.KindConflict) {
		return err
	}
	if have, lerr := reg.Lookup(ctx, want.Name); lerr == nil && have == want {
		return nil
	}
	return err
}

// onDiskHooksValue is the canonical [hooks] family value of the config file
// on disk (read for attention only, never to admit anything).
func onDiskHooksValue(ctx context.Context, w *daemonWiring) (json.RawMessage, error) {
	cf, err := runtime.ReadConfigFile(w.Paths.ConfigPath())
	if err != nil {
		return nil, err
	}
	cfg, err := cf.Parse(ctx, envFreeLoadOptions())
	if err != nil {
		return nil, err
	}
	return hooksFamilyExtract(cfg.Extra)
}

// raiseUnacceptedHookEdit files the attention item when the on-disk [hooks]
// would loosen the admitted section at boot (an edit nobody accepted).
func raiseUnacceptedHookEdit(ctx context.Context, w *daemonWiring, attention *supervision.Store) {
	disk, err := onDiskHooksValue(ctx, w)
	if err != nil {
		return
	}
	admitted, err := hooksFamilyExtract(w.Deps.Reloader.Current().Extra)
	if err != nil {
		return
	}
	if paths, err := hooksFamilyCompare(admitted, disk); err == nil && len(paths) > 0 {
		pushRefusedHookEdit(ctx, w, attention, disk)
	}
}

// pushRefusedHookEdit files one KindElevationRefused item per distinct edit.
func pushRefusedHookEdit(ctx context.Context, w *daemonWiring, attention *supervision.Store, disk json.RawMessage) {
	sum := sha256.Sum256(bytes.TrimSpace(disk))
	_, err := attention.Push(ctx, supervision.AttentionItem{Kind: supervision.KindElevationRefused,
		SourceRef: "hooks:" + hex.EncodeToString(sum[:])[:16], ScopeRef: scope.Ref{Kind: scope.ScopeKindGlobal}})
	if err != nil && w.Logger != nil {
		w.Logger.Error("hooks: refused hook edit not filed", "err", cascade.Wrap(cascade.KindUnavailable, err, "attention push").Error())
	}
}

// watchRefusedHookEdits files an item for each config.reload.rejected whose
// loosening paths name a hooks key.
func watchRefusedHookEdits(ctx context.Context, w *daemonWiring, p *hooksParts) error {
	defer func() { _ = p.rejects.Unsubscribe() }()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-p.rejects.Events:
			if !ok {
				return nil
			}
			if ev.Kind == "config.reload.rejected" && namesHooksLoosening(ev.Payload) {
				if disk, err := onDiskHooksValue(ctx, w); err == nil {
					pushRefusedHookEdit(ctx, w, p.attention, disk)
				}
			}
		}
	}
}

// namesHooksLoosening reports whether a rejection payload's loosening paths
// include hooks.action or hooks.vault_binding.
func namesHooksLoosening(payload []byte) bool {
	var body struct {
		Paths []struct{ Key string } `json:"loosening_paths"`
	}
	if json.Unmarshal(payload, &body) != nil {
		return false
	}
	return slices.ContainsFunc(body.Paths, func(p struct{ Key string }) bool {
		return p.Key == hooksActionKey || p.Key == hooksBindingKey
	})
}
