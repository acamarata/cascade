//go:build !windows

// Purpose: closes the P1-E24-W5-S50-T4 round-2 confirming-review REWORK's
//
//	composition-root gap (Q3/D1): the cascade-pa conversational install
//	flow's adapters (installerAdapter, approvalConfirmGate, dbEventBus)
//	were each opening their OWN second sqlitestore.Open against the
//	daemon's cascade.db (refused in-process -- providers/sqlite.Open's own
//	§D-3 exclusive flock) and building a PRIVATE ApprovalQueue unreachable
//	from `cascade approval grant/deny` (which drives the daemon's real
//	queue, a different instance). This file is the one call site that
//	hands the composition root's own already-built store/db/queue/registry
//	to that package instead, matching review_mount.go's identical
//	"new sibling wiring file, one-line hook, no RPC import needed" shape
//	for a composition-root gap that cannot be closed from inside the
//	ticket's own files_scope (internal/plugins/cascadepa_install_*.go
//	cannot see cmd/cascade's wirePolicy/openRuntimeStore).
//
// Inputs: the real provider.Store daemon_unix_store.go's openRuntimeStore
//
//	opened, and the policyWiring wirePolicy built (daemon_unix_policy.go)
//	-- specifically its Queue and Registry fields, the SAME two values
//	policy.RPCDeps hands policy.MethodHandlers for the approval.* verbs
//	(daemon_unix_policy.go's wirePolicy: "wiring.Handlers =
//	policy.MethodHandlers(policy.RPCDeps{Queue: queue, ... Registry:
//	registry, ...})").
//
// Outputs: the *plugins.InstallHostDeps this call injected -- returned so
//
//	a same-package test can assert its exported fields directly (proving
//	the composition root wires the RIGHT values through) without a
//	second, test-only probe symbol in internal/plugins
//	(round-3 rework, T0 decision D2: internal/plugins.InstallHostDepsConfigured
//	was deleted -- a boolean-only symbol with no production caller,
//	failing internal/build's TestTestOnlyUsage_RealTreeGreen gate).
//	platformDaemonRun itself (below) still discards it -- this remains a
//	pure injection call (internal/plugins.SetInstallHostDeps) as far as
//	the daemon's own startup path is concerned; it imports no internal/rpc
//	symbol and therefore needs no cmd/rpc-boundary exemption
//	(LANE-RULES.md §17).
//
// Constraints: called from platformDaemonRun (daemon_unix.go) BEFORE
//
//	buildRPCServer, so InstallHostDeps is set before the daemon can accept
//	its first RPC connection -- and therefore before RunIntent could ever
//	reach the install flow's adapters. daemon_unix.go itself is outside
//	this ticket's declared files_scope (internal/plugins/
//	cascadepa_install_*.go, plugins/cascade-pa/install/**,
//	plugins/cascade-pa/commands.go's DispatchIntent hunk, this new file);
//	the one-line call site addition there is reported as a scope
//	deviation per LANE-RULES.md §4 -- it is the ONLY place store, the raw
//	db and the policy composition root's Queue/Registry are all in scope
//	together (platformDaemonRun), and T0's own standing composition-root
//	ruling (LANE-RULES.md §19, precedents S-48.T1/T3, S-51.T4, S-50.T4)
//	requires the real caller to exist rather than parking this behind an
//	allow entry.
//
// SPORT: cmd/cascade:cascadepa-install-hostdeps (ADD) -- FIX P1-E24-W5-S50-T4 (D1).
package main

import (
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/provider"
)

// wireCascadePAInstallHostDeps injects the daemon's own already-opened
// cascade.db store and the SAME policy approval queue and capability
// registry the approval.* RPC handlers use into the cascade-pa
// conversational install flow's composition root, so its adapters share
// the daemon's single cascade.db connection and approval queue instead of
// each opening -- and conflicting over, or being unreachable from,
// -- a second one. See internal/plugins/cascadepa_install_shared_store.go's
// header for the conflict this closes. Returns the injected
// *plugins.InstallHostDeps so a caller (this file's own test) can assert
// on it directly.
func wireCascadePAInstallHostDeps(store provider.Store, queue policy.ApprovalQueue, registry policy.CapabilityRegistry) *plugins.InstallHostDeps {
	deps := &plugins.InstallHostDeps{Store: store, Queue: queue, Registry: registry}
	plugins.SetInstallHostDeps(deps)
	return deps
}
