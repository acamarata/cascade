//go:build !windows

// Purpose: registers the delegate daemon wiring. At start it applies the
// delegation schema, offers the configured agent lanes to the fleet, runs
// spawn recovery and mounts conductor.delegate and conductor.delegate_wait.
// The service itself (installation-key signer, host shim, result collection)
// is built on the first delegate call, once the notify runtime exists.
// Inputs: the daemon wiring (fleet runtime, config, paths, store, manifest).
// Outputs: the two verbs over a delegateRuntime; daemonSpawns tracks every spawn.
// Constraints: never arms the owner-peer guard (the RPC server does); RepoIDOf
// stays nil; a wiring without a fleet or config serves both verbs as KindUnavailable.
package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/context/policy"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/delegation"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/providers/agents"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
	scopeenvelope "github.com/acamarata/cascade/pkg/envelope"
	"github.com/acamarata/cascade/pkg/provider"
)

// ownerHopPrefix names the first-hop record the owner CLI delegates through.
const ownerHopPrefix = "owner-hop-"

var _ = registerDaemonWiring(daemonRegistration{
	Name: "delegate", Phase: phaseConductor, Order: 50,
	Wire: func(w *daemonWiring) error {
		rt, err := newDelegateRuntime(w)
		if err != nil {
			return err
		}
		mountDelegate(w.Registry, rt)
		return nil
	},
})

// delegateRuntime is the wired state behind both verbs. build makes the
// service and the owner-root signer; a nil build refuses with reason.
type delegateRuntime struct {
	mu        sync.Mutex
	svc       *delegation.Service
	signer    scopeenvelope.Signer
	build     func(ctx context.Context) (*delegation.Service, scopeenvelope.Signer, error)
	reason    string
	store     *jobs.Store
	clock     runtime.Clock
	base      string
	wallClock time.Duration
}

// newDelegateRuntime runs the start-time half: schema, lanes and recovery.
func newDelegateRuntime(w *daemonWiring) (*delegateRuntime, error) {
	rt := &delegateRuntime{clock: w.Clock, reason: "no fleet runtime or config"}
	fleet := w.Deps.Fleet
	if fleet == nil || fleet.Fleet() == nil || fleet.Jobs() == nil || w.Deps.Config == nil {
		return rt, nil
	}
	if err := jobs.ApplyDelegationSchema(w.Ctx, fleet.JobsDB(), migrate.SQLiteEmitter{}, w.Clock, "", ""); err != nil {
		return nil, err
	}
	cfg, err := runtime.ParseAgentsConfig(w.Deps.Config.Extra)
	if err != nil {
		rt.reason = err.Error()
		return rt, nil
	}
	lanes := delegateLanes(cfg)
	fleet.SetAgentLanes(func() []capacity.AgentLane { return lanes })
	rt.store, rt.base, rt.wallClock = fleet.Jobs(), filepath.Join(w.Paths.DataDir(), "delegate"), cfg.Delegation.WallClock
	effects, err := openEffectLog(w.Store, w.Clock, nil, w.Paths)
	if err != nil {
		rt.reason = err.Error()
		return rt, nil
	}
	err = delegation.RecoverPendingSpawns(w.Ctx, delegation.RecoveryDeps{Effects: effects, Store: rt.store,
		Fleet: fleet.Fleet(), ResolveLease: resolveRecoveredLease, ReleaseWorktree: rt.releaseOwnerWorktree})
	if err != nil && w.Logger != nil {
		w.Logger.Warn("delegate: spawn recovery left records live", "error", err.Error())
	}
	rt.build = func(ctx context.Context) (*delegation.Service, scopeenvelope.Signer, error) {
		return buildDelegateService(ctx, w, cfg, lanes)
	}
	return rt, nil
}

// buildDelegateService builds the service over the installation key and the host shim.
func buildDelegateService(ctx context.Context, w *daemonWiring, cfg runtime.AgentsConfig, lanes []capacity.AgentLane) (*delegation.Service, scopeenvelope.Signer, error) {
	if w.Deps.Notify == nil || w.Deps.Minter == nil {
		return nil, nil, cascade.New(cascade.KindUnavailable, "conductor.delegate: no notify runtime or principal minter is wired")
	}
	signer, err := productionEnvelopeSigner(ctx, w.Paths)
	if err != nil {
		return nil, nil, err
	}
	shim, err := buildDelegateShim(w, cfg)
	if err != nil {
		return nil, nil, err
	}
	svc := delegation.NewService(delegation.Deps{Clock: w.Clock, Store: w.Deps.Fleet.Jobs(), Pinned: signer.PublicKey(),
		RepoIDOf: delegateRepoIDOf(scope.NewGraphStore(w.Deps.RawDB), cascadecontext.GitCommonDir),
		Signer:   signer, Minter: w.Deps.Minter, Shim: registeringShim{spawn: shim, live: daemonSpawns}}.WithFleet(w.Deps.Fleet.Fleet()))
	svc.SetAgentLanes(lanes)
	err = svc.BindCollection(delegation.Collection{Ctx: w.Ctx, Supervisor: w.Manifest, Notify: w.Deps.Notify.Service(),
		Ticks: func() runtime.Ticker { return runtime.NewSystemTicker(time.Second) }, MaxWallClock: cfg.Delegation.MaxWallClock})
	if err != nil {
		return nil, nil, err
	}
	return svc, signer, nil
}

// buildDelegateShim builds the host shim over the configured drivers.
func buildDelegateShim(w *daemonWiring, cfg runtime.AgentsConfig) (*agents.Shim, error) {
	resolver, err := agents.NewResolver(builtinPluginPresence())
	if err != nil {
		return nil, err
	}
	bindings := agents.Bindings()
	auth, err := agents.ParseDriverAuth(w.Deps.Config.Extra, bindings)
	if err != nil {
		return nil, err
	}
	effects, err := openEffectLog(w.Store, w.Clock, nil, w.Paths)
	if err != nil {
		return nil, err
	}
	log, err := openDaemonAuditLog(w.Store, w.Clock, nil, w.Paths)
	if err != nil {
		return nil, err
	}
	executable := os.Executable
	if w.Deps.Daemon.Executable != nil {
		executable = w.Deps.Daemon.Executable
	}
	exe, err := executable()
	if err != nil {
		return nil, err
	}
	getenv := func(string) string { return "" }
	if w.Deps.Creds != nil {
		getenv = w.Deps.Creds.Getenv
	}
	return agents.NewShim(agents.ShimDeps{Resolver: resolver, Auth: auth, Effects: effects,
		ProfileBase:  filepath.Join(w.Paths.DataDir(), "agents", "profiles"),
		Engine:       func() (*egress.Engine, error) { return daemonEgressFirewall(w.Paths, egress.DefaultRegistry()) },
		ProxyJournal: agents.ProxyJournal(log, effects), Binaries: delegateBinaries(bindings, cfg),
		Defaults: map[agents.HarnessID]provider.DriverDefaults{"claude": func() provider.DriverDefaults {
			d := provider.NewDriverDefaults()
			d.KernelPromptFile = filepath.Join(w.Paths.DataDir(), policy.KernelFileRel)
			d.SubagentKernelPromptFile = filepath.Join(w.Paths.DataDir(), policy.KernelFileRel)
			return d
		}()},
		Exe: exe, Getenv: getenv, Clock: w.Clock})
}

// delegateLanes offers every enabled lane; only a programmatic posture may spawn.
func delegateLanes(cfg runtime.AgentsConfig) []capacity.AgentLane {
	var out []capacity.AgentLane
	for _, l := range cfg.Lanes {
		if l.Enabled {
			out = append(out, capacity.AgentLane{DriverID: l.Driver, LaneType: conductor.LaneExternalAPI,
				Posture: provider.CompliancePosture{ProgrammaticEntitlement: l.Posture == runtime.AgentPostureProgrammatic,
					InteractiveEntitlement: l.Posture == runtime.AgentPostureInteractive}})
		}
	}
	return out
}

// delegateBinaries maps each enabled lane's harness to its first vendor
// executable found on PATH; a harness with none is left out (its spawn refuses).
func delegateBinaries(bindings []agents.Binding, cfg runtime.AgentsConfig) map[agents.HarnessID]string {
	enabled := map[agents.HarnessID]bool{}
	for _, l := range cfg.Lanes {
		enabled[agents.HarnessID(l.Driver)] = enabled[agents.HarnessID(l.Driver)] || l.Enabled
	}
	out := map[agents.HarnessID]string{}
	for _, b := range bindings {
		for _, name := range b.Binaries {
			if p, err := exec.LookPath(name); enabled[b.Harness] && err == nil && filepath.IsAbs(p) && out[b.Harness] == "" {
				out[b.Harness] = p
			}
		}
	}
	return out
}

// builtinPluginPresence reports a plugin present when it is a loaded builtin.
func builtinPluginPresence() agents.PluginPresence {
	reg := &plugins.BuiltinRegistry{}
	present := map[string]bool{}
	if reg.Load() == nil {
		for _, r := range reg.List() {
			present[r.Manifest.ID] = true
		}
	}
	return func(id string) bool { return present[id] }
}

// resolveRecoveredLease: a record that never leased has nothing to release. A
// leased one cannot be rebuilt (the record keeps only the lease id), so its
// cleanup is refused and the record stays live for a later recovery.
func resolveRecoveredLease(_ context.Context, rec jobs.DelegationRecord) (capacity.Lease, bool, error) {
	if rec.LeaseID == "" {
		return capacity.Lease{}, false, nil
	}
	return capacity.Lease{}, false, cascade.Newf(cascade.KindUnavailable, "delegate: lease %s of %s cannot be rebuilt after a restart", rec.LeaseID, rec.ID)
}

// releaseOwnerWorktree removes an owner hop's read-only worktree; any other record holds none.
func (rt *delegateRuntime) releaseOwnerWorktree(_ context.Context, rec jobs.DelegationRecord) error {
	id, ok := strings.CutPrefix(rec.AgentPrincipal, ownerHopPrefix)
	if !ok || !filepath.IsLocal(id) || strings.ContainsAny(id, `/\`) {
		return nil
	}
	return (&delegation.Worktree{Root: filepath.Join(rt.base, "ro-"+id)}).Release()
}

// parent resolves the record a delegation hangs from: a principal caller's own
// record, or a fresh owner hop under a read-only worktree for the owner CLI.
func (rt *delegateRuntime) parent(ctx context.Context, policy string) (jobs.DelegationRecord, *delegation.Worktree, func(context.Context, error) error, error) {
	pass := func(_ context.Context, err error) error { return err }
	c, _ := principal.CallerFrom(ctx)
	switch {
	case c.Principal != nil:
		rec, err := callerParent(ctx, rt.store, c)
		return rec, nil, pass, err
	case !c.OwnerCLI:
		return jobs.DelegationRecord{}, nil, pass, cascade.New(cascade.KindPermissionDenied, "conductor.delegate: the caller is neither a principal nor the owner CLI")
	}
	id, err := newDelegateID("")
	if err != nil {
		return jobs.DelegationRecord{}, nil, pass, err
	}
	wt, err := delegation.AllocWorktree(rt.base, id, false)
	if err != nil {
		return jobs.DelegationRecord{}, nil, pass, err
	}
	rec, err := rt.ownerHop(ctx, id, wt.Root, policy)
	if err != nil {
		_ = wt.Release()
		return jobs.DelegationRecord{}, nil, pass, err
	}
	finish := func(ctx context.Context, err error) error {
		if err != nil {
			_ = rt.store.FinishDelegation(context.WithoutCancel(ctx), rec.ID, jobs.DelegationRefused, err.Error())
		}
		return err
	}
	return rec, wt, finish, nil
}

// ownerHop signs an owner root over dir, stores its first hop and returns it.
func (rt *delegateRuntime) ownerHop(ctx context.Context, id, dir, policy string) (jobs.DelegationRecord, error) {
	now := rt.clock.Now()
	src := delegation.Sources{ReadOnlyDir: dir, ToolPolicy: policy, Sensitivity: provider.SensitivityInternal,
		Budget: scopeenvelope.Budget{WallClock: rt.wallClock}, Now: now, TTL: rt.wallClock}
	root, err := delegation.MintRoot(ctx, rt.signer, principal.Principal{ID: delegation.OwnerCLI}, src)
	if err != nil {
		return jobs.DelegationRecord{}, err
	}
	hop, err := delegation.MintChild(ctx, rt.signer, root, rt.signer.PublicKey(), ownerHopPrefix+id, src)
	if err != nil {
		return jobs.DelegationRecord{}, err
	}
	rootB, err := json.Marshal(root)
	if err != nil {
		return jobs.DelegationRecord{}, cascade.Wrap(cascade.KindInternal, err, "conductor.delegate: encode the owner root")
	}
	hopB, err := json.Marshal(hop)
	if err != nil {
		return jobs.DelegationRecord{}, cascade.Wrap(cascade.KindInternal, err, "conductor.delegate: encode the owner hop")
	}
	if err := rt.store.InsertDelegation(ctx, jobs.DelegationRecord{ID: hop.Env.ID, Signed: hopB, ParentSigned: rootB,
		AgentPrincipal: hop.Env.Principal, State: jobs.DelegationLive, CreatedAt: now}); err != nil {
		return jobs.DelegationRecord{}, err
	}
	return rt.store.GetDelegation(ctx, hop.Env.ID)
}
