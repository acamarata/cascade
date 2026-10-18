//go:build !windows

// Purpose: the daemon run path's POLICY composition root. It takes the one
//
//	production policy.Engine this process evaluates every gated action
//	through from newPolicyEngine (policy_engine.go), over the B-layer seams
//	only, and builds the single authorization middleware
//	(routing.ActionRouter) the scheduler and the hook dispatcher share.
//
// Inputs: the real provider.Store openRuntimeStore opened, the process
//
//	Clock, and the loaded *runtime.Config the [policy] section is read
//	from.
//
// Outputs: the boot-loaded capability registry, the production Engine,
//
//	and the ActionRouter over it and the real append-only audit log.
//
// Constraints: B-layer seams only. The grant store is StoreGrants over
//
//	provider.Store (never an in-memory test store), the deny-list is
//	StoreDenyList over the same store with its rows loaded at boot, and
//	the autonomy profile comes from the running config. Nothing here
//	installs a permissive default: an engine this file could not build
//	fully is returned as an error, never as a partially wired engine that
//	would answer by omission.
//
// SPORT: cmd/cascade/daemon (CHANGED — policy composition root).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/events/routing"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// The capabilities this composition root routes its own gated call sites
// as. A capability the registry does not hold denies terminally inside the
// evaluator, so every call site this process installs a gate for must
// register its capability here at boot.
const (
	// schedulerCapability is the capability a cron-triggered dispatch of a
	// registered runnable is evaluated against.
	//
	// No hook capability is registered alongside it. This process
	// constructs no hooks.Dispatcher at all, so a hooks.shell row here
	// would be a policy-surface claim with no call site behind it.
	schedulerCapability = "scheduler.dispatch"
)

// schedulerSubjectID identifies the daemon's own scheduler as a principal.
// It is an agent rather than a user: nobody is at the keyboard when a cron
// job fires, and attributing the dispatch to a user in the audit trail
// would put a person's name on an action they did not take.
const schedulerSubjectID = "daemon-scheduler"

// schedulerSubject is the principal scheduled dispatches are evaluated as.
func schedulerSubject() policy.Subject {
	return policy.Subject{Kind: policy.SubjectAgent, ID: schedulerSubjectID}
}

// bootCapabilities is the capability set this composition root registers
// at boot. Each entry states the action class the capability confers; the
// class can only RAISE the level an action is evaluated at, never lower
// it, so a class named here can never widen what the classifier resolved.
func bootCapabilities() []policy.Capability {
	caps := []policy.Capability{
		{
			Name:          schedulerCapability,
			Desc:          "dispatch a registered scheduled runnable",
			DefaultPolicy: policy.ClassLocalDev,
		},
	}
	// The MCP tool capabilities (P1-E16-W4-S34-T2) are registered HERE,
	// in the daemon's one capability registry, rather than in a second
	// registry of their own. An unregistered capability denies, so an MCP
	// tool asking about a capability this registry has never heard of
	// would be withheld for a reason that has nothing to do with the
	// operator's grants — and `cascade policy grant memory.write` would
	// have nothing to grant against.
	// The GitHub CI capabilities (P1-E25-W5-S51-T3) are registered here for
	// the same reason: `cascade github ci merge-on-green` is an L3 action
	// whose rung comes from its capability's registered class (R-14.211),
	// and an operator must be able to grant it by name.
	caps = append(caps, githubCICapabilities()...)
	return append(caps, mcpToolCapabilities()...)
}

// policyWiring is what wirePolicy built, named as a set so a caller cannot
// take the router without the engine that answers for it.
type policyWiring struct {
	// policyEngineParts carries the engine every gated call site evaluates
	// through and the collaborators it was built over: the capability
	// registry singleton, the grant store, the stored deny list, the
	// autonomy controller and the process's one redacting audit log
	// (openAuditLog), which the router, the approval queue and supervision
	// share.
	policyEngineParts
	// Router is the single authorization middleware every gated call site
	// in this process shares.
	Router *routing.ActionRouter
	// Queue is the ONE approval queue in this process. The engine files
	// its ask verdicts here and the approval.* verbs read the same value,
	// so an operator listing the queue sees the entries the engine
	// actually made rather than a second, empty queue.
	Queue policy.ApprovalQueue
	// Handlers is the approval/policy JSON-RPC handler set built over
	// everything above. The RPC composition root registers it.
	Handlers map[string]policy.MethodFunc
}

// wirePolicy constructs the production policy engine and the one
// authorization middleware over it.
//
// # Which seams are real here, and which are not
//
// The grant store, the deny-list and the autonomy profile are the B-layer
// implementations, not defaults: DefaultDenyList (which holds no
// configured rows) is replaced by StoreDenyList with its rows loaded from
// the policy domain at boot, and the controller is given the running
// config so the profile is the operator's rather than none at all.
//
// The capability registry is MemoryRegistry populated at boot from the set
// above. No store-backed CapabilityRegistry implementation exists in this
// tree, so "populated from the B-layer store" is not yet available; the
// singleton this returns is the one every call site shares, which is the
// part that matters for a decision, and the persistence half is recorded
// against the ticket that adds the store-backed registry.
//
// The layer-1 same-turn authorizer and the layer-2 elevation verifier are
// deliberately left at their fail-closed defaults. No production
// implementation of either exists, and attaching a placeholder would make
// an unauthorized action report success.
func wirePolicy(ctx context.Context, store provider.Store, clock runtime.Clock, cfg *runtime.Config, paths runtime.PathProvider) (*policyWiring, error) {
	// Bind the process firewall to the daemon's own data directory before
	// the constructor opens the audit log, so which wiring step reaches the
	// log first never decides whether the vault is the daemon's. (A
	// malformed [policy] now selects vault custody before it refuses; the
	// refusal itself is unchanged.)
	if _, err := bindDaemonFirewall(paths); err != nil {
		return nil, err
	}
	// The daemon loads its config with runtime.Load, which reads a
	// config.toml it cannot follow as "no config file". Refuse that here, as
	// the MCP and github-ci surfaces do, rather than run the default profile.
	if err := refuseUnfollowableConfig(paths); err != nil {
		return nil, err
	}
	parts, err := newPolicyEngine(ctx, policyEngineInputs{
		Store: store, Clock: clock, Config: cfg, Capabilities: bootCapabilities(), Paths: paths,
	})
	if err != nil {
		return nil, err
	}
	queue, err := buildApprovalQueue(store, parts.Registry, parts.Grants, parts.DenyList, clock, parts.Audit)
	if err != nil {
		return nil, err
	}
	engine := parts.Engine.WithApprovalQueue(queue)
	router, err := routing.NewActionRouter(engine, parts.Audit)
	if err != nil {
		return nil, err
	}
	router, err = attachAutoAdvance(router, engine, parts.Controller, store, clock, parts.Audit, cfg)
	if err != nil {
		return nil, err
	}
	wiring := &policyWiring{policyEngineParts: parts, Router: router, Queue: queue}
	wiring.Handlers = policy.MethodHandlers(policy.RPCDeps{
		Queue:    queue,
		Engine:   engine,
		Registry: parts.Registry,
		Grants:   parts.Grants,
		DenyList: parts.DenyList,
		Audit:    parts.Audit,
		Clock:    clock,
		// Verifier and Attestor are deliberately absent. No production
		// approval-key source and no production attestation helper exist
		// in this tree, and both absences are fail-closed: approval.grant
		// refuses a token nothing can verify, and every elevated verb
		// returns ElevationRequired. Attaching a placeholder to either
		// would make an unauthorized redemption report success.
	})
	return wiring, nil
}

// buildApprovalQueue constructs the approval queue and its single-use
// ledger over the same store, registry, grants and deny-list the engine
// itself was built from. It is split out of wirePolicy only to keep that
// function under Art.10.3's 50-line cap: passing the SAME collaborator
// values on is the point, since a queue built over a second registry or a
// second grant store could answer a revocation question differently from
// the engine that consults it.
func buildApprovalQueue(
	store provider.Store, registry *policy.MemoryRegistry, grants *policy.StoreGrants,
	denyList *policy.StoreDenyList, clock runtime.Clock, log *audit.Log,
) (*policy.StoreApprovals, error) {
	return policy.NewApprovalQueue(policy.ApprovalQueueConfig{
		Store:    store,
		Registry: registry,
		Grants:   grants,
		Clock:    clock,
		Recorder: log,
		DenyList: denyList,
	})
}
