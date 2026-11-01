//go:build !windows

// Purpose: the daemon's one conductor quota policy and owner deny store,
//
//	both read from the HotReloader's current (effective) config: the deny
//	store on every call, the quota re-applied on every accepted reload.
//
// Inputs: the daemon's HotReloader, clock, Manifest and logger.
// Outputs: *conductorPolicy (daemonRuntime.ConductorPolicy), the
//
//	"conductor.policy" status entry, and the lookup the conductor wiring
//	reads the policy through (conductorPolicyFor, keyed by config path:
//	wire_conductor.go hands the wiring paths, not daemonRuntime).
//
// Constraints: fail closed. A deny value that cannot be read or names an
//
//	unknown class refuses every class, never allows; at boot it refuses
//	start. A reload whose [conductor.quota] does not parse keeps the
//	previous quota and logs the key. Loosening reaches Current() only
//	through config.accept_loosening (the guarded conductor family).
//
// SPORT: cmd/cascade/daemon:conductor-quota-deny (ADD, P1-CAP-07).
package main

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// conductorPolicyStatus is the Manifest entry status.get shows the deny and
// quota state under.
const conductorPolicyStatus = "conductor.policy"

// ownerDenyStore is the production conductor.DenyStore over
// [conductor].deny_task_classes. live is read on every call.
type ownerDenyStore struct {
	live    func() *runtime.Config
	classes conductor.TaskClassRegistry
}

// DeniedTaskClass refuses a listed class, and every class when the deny
// value cannot be read: the cause is the reason, never an allow.
func (s *ownerDenyStore) DeniedTaskClass(_ context.Context, taskClass string) (string, bool) {
	listed, err := s.read()
	if err != nil {
		return "deny configuration unavailable: " + err.Error(), true
	}
	if slices.Contains(listed, taskClass) {
		return "denied by [conductor].deny_task_classes", true
	}
	return "", false
}

// read decodes the live deny list and checks every name against the task
// class table.
func (s *ownerDenyStore) read() ([]string, error) {
	if s.live == nil {
		return nil, cascade.Newf(cascade.KindUnavailable, "%s: no live configuration", runtime.ConductorDenyKey)
	}
	return denyList(s.classes, s.live())
}

// denyList decodes cfg's deny list and checks every name against classes.
func denyList(classes conductor.TaskClassRegistry, cfg *runtime.Config) ([]string, error) {
	listed, err := runtime.ConductorDenyTaskClasses(cfg)
	if err != nil {
		return nil, err
	}
	known := classes.Classes()
	for _, c := range listed {
		if !slices.Contains(known, c) {
			return nil, cascade.Newf(cascade.KindInvalidInput, "%s: unknown task class %q (known: %s)",
				runtime.ConductorDenyKey, c, strings.Join(known, ", "))
		}
	}
	return listed, nil
}

// denyDetail is the status.get line for the deny list of d.
func denyDetail(d conductor.DenyStore) string {
	s, ok := d.(*ownerDenyStore)
	if !ok {
		return "deny_task_classes: unavailable (refusing)"
	}
	listed, err := s.read()
	switch {
	case err != nil:
		return "deny_task_classes: unavailable (refusing)"
	case len(listed) == 0:
		return "deny_task_classes: none configured"
	}
	return "deny_task_classes: " + strings.Join(listed, ", ")
}

// buildConductorPolicy is the one construction of the daemon's QuotaPolicy
// and DenyStore, both over live.
func buildConductorPolicy(live func() *runtime.Config, clock runtime.Clock) (*conductorPolicy, error) {
	q, err := liveQuotaConfig(live)
	if err != nil {
		return nil, err
	}
	return &conductorPolicy{Quota: conductor.NewQuotaPolicy(q, clock), Deny: &ownerDenyStore{live: live}}, nil
}

// liveQuotaConfig parses [conductor.quota] out of live().
func liveQuotaConfig(live func() *runtime.Config) (conductor.QuotaConfig, error) {
	var extra map[string]interface{}
	if cfg := live(); cfg != nil {
		extra = cfg.Extra
	}
	q, _, err := conductor.ParseQuotaConfig(extra)
	return q, err
}

// liveConductor carries the policy from the reload wiring (which owns the
// HotReloader) to composeDaemon and the conductor wiring.
type liveConductor struct {
	reloader   *runtime.HotReloader
	configPath string
	manifest   *daemon.Manifest
	log        *slog.Logger
	policy     atomic.Pointer[conductorPolicy]
}

// attach builds the policy over hr.Current(), refusing start on a deny or
// quota value it cannot read, and publishes it for the conductor wiring of
// the daemon whose config is configPath.
func (l *liveConductor) attach(hr *runtime.HotReloader, onDisk *runtime.Config, configPath string, clock runtime.Clock, manifest *daemon.Manifest, log *slog.Logger) error {
	// The file's own values first: a guarded value the baseline replaced
	// must still refuse start, never boot with no deny at all.
	if err := checkConductorValues(onDisk); err != nil {
		return err
	}
	p, err := buildConductorPolicy(hr.Current, clock)
	if err != nil {
		return err
	}
	deny, ok := p.Deny.(*ownerDenyStore)
	if !ok {
		return cascade.New(cascade.KindInternal, "conductor: the deny store is not the owner deny store")
	}
	if _, err := deny.read(); err != nil {
		return err
	}
	l.reloader, l.configPath, l.manifest, l.log = hr, configPath, manifest, log
	l.policy.Store(p)
	livePolicies.set(configPath, l, p)
	l.reloaded(context.Background()) // a reload accepted before the Store above
	return nil
}

// checkConductorValues refuses a deny list or [conductor.quota] in cfg that
// does not parse, or a deny naming an unknown task class.
func checkConductorValues(cfg *runtime.Config) error {
	if cfg == nil {
		return nil
	}
	if _, err := denyList(conductor.TaskClassRegistry{}, cfg); err != nil {
		return err
	}
	_, err := liveQuotaConfig(func() *runtime.Config { return cfg })
	return err
}

// reloaded re-applies [conductor.quota] from the reloader's current config;
// a value that does not parse keeps the previous quota and is logged.
func (l *liveConductor) reloaded(context.Context) {
	p := l.policy.Load()
	if p == nil {
		return
	}
	if q, err := liveQuotaConfig(l.reloader.Current); err != nil {
		if l.log != nil {
			l.log.Warn("conductor quota reload refused; the previous quota stays", slog.String("key", "conductor.quota"), slog.String("reason", err.Error()))
		}
	} else {
		p.Quota.Apply(q)
	}
	l.report(p)
}

// report records the deny state on the Manifest: Failed while unreadable.
func (l *liveConductor) report(p *conductorPolicy) {
	if l.manifest == nil {
		return
	}
	d := denyDetail(p.Deny)
	if strings.HasSuffix(d, "(refusing)") {
		l.manifest.Failed(conductorPolicyStatus, d)
		return
	}
	l.manifest.Started(conductorPolicyStatus, d)
}

// detach removes the policy from the lookup at shutdown.
func (l *liveConductor) detach() {
	livePolicies.drop(l.configPath, l)
}

// livePolicies maps a daemon's config path to its policy.
var livePolicies = policyLookup{byConfig: map[string]*conductorPolicy{}, owner: map[string]*liveConductor{}}

type policyLookup struct {
	mu       sync.Mutex
	byConfig map[string]*conductorPolicy
	owner    map[string]*liveConductor
}

func (l *policyLookup) set(path string, owner *liveConductor, p *conductorPolicy) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.byConfig[path], l.owner[path] = p, owner
}

// drop removes path's entry only while owner still holds it, so a restarted
// daemon's entry survives the previous one's late cleanup.
func (l *policyLookup) drop(path string, owner *liveConductor) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.owner[path] == owner {
		delete(l.byConfig, path)
		delete(l.owner, path)
	}
}

// conductorPolicyFor returns the composed daemon's policy for paths. A
// wiring built without composeDaemon has no reloader: it gets a policy whose
// deny store has no live configuration, so every class is refused.
func conductorPolicyFor(paths runtime.PathProvider, clock runtime.Clock) (*conductorPolicy, error) {
	if paths != nil {
		livePolicies.mu.Lock()
		p := livePolicies.byConfig[paths.ConfigPath()]
		livePolicies.mu.Unlock()
		if p != nil {
			return p, nil
		}
	}
	return buildConductorPolicy(func() *runtime.Config { return nil }, clock)
}

// withLiveConductor hands the policy and its reloader to every registration.
func withLiveConductor(opt rpcServerOption, l *liveConductor) rpcServerOption {
	if opt.runtime != nil {
		opt.runtime.Reloader, opt.runtime.ConductorPolicy = l.reloader, l.policy.Load()
	}
	return opt
}
