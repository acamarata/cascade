//go:build !windows

// Purpose: registers policy.authorize on the daemon registry and attaches the
// policy engine constructor's same-turn ledger to the daemon engine, the only
// engine it is ever attached to (the stdio MCP engine keeps NoSameTurnAuth).
// The verb is always elevated (internal/rpc elevation table) and declared
// owner-only in daemon_unix_policy_authorize.go, which also holds
// mountPolicyAuthorize (this file stays clear of internal/rpc).
// Inputs: w.Deps.Policy (set by composeDaemon from wirePolicy).
// Outputs: one registered method; the ledger attached as the engine's layer 1
// same-turn authorizer.
// Constraints: a wiring without a policy engine, ledger or audit log (a
// test-built registry) registers a handler that refuses, so the verb never
// answers as if it had recorded an authorization.
// SPORT: cmd/cascade policy.authorize wiring (P1-SEC-10).
package main

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

var _ = registerDaemonWiring(daemonRegistration{
	Name: "policy-authorize", Phase: phaseCore, Order: 30,
	Wire: func(w *daemonWiring) error {
		pol := w.Deps.Policy
		if pol == nil || pol.Engine == nil || pol.SameTurn == nil || pol.Audit == nil {
			w.Registry.Register(policyAuthorizeMethod, func(context.Context, json.RawMessage) (any, error) {
				return nil, cascade.New(cascade.KindUnavailable, "policy.authorize: this daemon has no policy engine")
			})
			return nil
		}
		mountPolicyAuthorize(w.Registry, pol.policyEngineParts, pol.Audit)
		return nil
	},
})
