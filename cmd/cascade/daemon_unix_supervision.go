//go:build !windows

// Purpose: the daemon run path's SUPERVISION wiring — the three stages an
//
//	ask passes through after the one policy evaluation: the mandatory
//	first-run dry-run gate (S-39.T4), the operator's configured
//	supervision tier (S-39.T3), and the tier-1 auto-advance stage
//	(S-39.T2).
//
// WHY IT IS SPLIT OUT. daemon_unix_policy.go reached Art.10.3's 300-line
//
//	cap. The split is along a real seam rather than an arbitrary one:
//	policy.go builds the ENGINE and the one authorization middleware, and
//	this file attaches what happens to an ask the engine decided to ask
//	about.
//
// Inputs: the production engine and controller policy.go built, the same
//
//	store/clock/audit writer every other policy record here uses, and the
//	loaded config the tier is read from.
//
// Outputs: the router, with all three stages attached, or a real error.
// Constraints: an install that cannot build a stage refuses the BOOT. A
//
//	daemon that started with the dry-run gate or the configured tier
//	silently absent would supervise less than the operator asked for and
//	report itself as healthy.
//
// SPORT: cmd/cascade/daemon (CHANGED — supervision stage wiring).

package main

import (
	"os"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/events/routing"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// attachAutoAdvance adds the tier-1 ask-resolution stage to router, behind
// the mandatory first-run dry-run gate (P1-E18-W4-S39-T2, P1-E18-W4-S39-T4).
//
// It reads the ceiling off the LIVE controller rather than a value captured
// here, so a config reload that turns auto-advance off is observed by the
// very next action rather than at the next daemon restart. With the ceiling
// at its default — disabled — this changes nothing about how any action is
// decided; it is the operator's opt-in that switches it on.
//
// The dry-run gate is wired in the same breath as the stage it guards: it
// wraps the ask-resolution path at the router (R-14.247 §7), simulates on
// the production engine this file built, and persists its first_run_done
// flags in the audit domain of the SAME store every other policy record
// here uses. It is not configurable off and has no switch; an install that
// could not build it refuses the boot rather than shipping the tier-1 path
// unguarded.
//
// The attention store is built over the SAME store/clock this composition
// root already holds, which is this repo's established pattern for it
// (daemon_unix_scheduler.go and internal/daemon/attention_rpc.go each build
// their own identically): they reach the one queue, not a second
// disconnected one. The event bus is not available here, and a nil bus
// degrades to no-SSE mode — a refusal is queued and listed, it just does
// not push a live notification.
func attachAutoAdvance(
	router *routing.ActionRouter, engine *policy.Engine, controller *policy.Controller,
	store provider.Store, clock runtime.Clock, log audit.Writer, cfg *runtime.Config,
) (*routing.ActionRouter, error) {
	attention := supervision.NewStore(store, clock, nil, supervision.NewSystemIDGenerator(), 0)
	flags, err := supervision.NewFirstRunFlags(store)
	if err != nil {
		return nil, err
	}
	gate, err := supervision.EnforceDryRunFirst(engine, flags, controller, attention, log)
	if err != nil {
		return nil, err
	}
	tiers, err := attachTierSupervision(cfg, attention, log)
	if err != nil {
		return nil, err
	}
	// The fleet counters observe the SAME recorder every verdict already
	// travels through (P1-E18-W4-S40-T3). Counting here rather than off a
	// bus event means the number is taken at the one sink that sees every
	// verdict, and no event kind had to be invented to carry it.
	_, metrics, err := daemonMetrics()
	if err != nil {
		return nil, err
	}
	recorder := supervision.NewAutoAdvanceRecorder(log, attention, supervision.ScopeRef{}).
		WithObserver(metrics)
	return router.WithAutoAdvance(
		supervision.NewAutoAdvanceEvaluator(controller),
		recorder,
	).WithDryRunFirst(gate).WithTierSupervision(tiers), nil
}

// attachTierSupervision builds the configured-tier dispatcher
// (P1-E18-W4-S39-T3).
//
// The tier is read from [fleet.supervision].tier ONCE here and then held by
// the closure the dispatcher reads live. It is parsed at boot rather than
// per action for the reason every other section is: a malformed section
// must refuse the daemon's start, where an operator is watching, rather
// than surface as a strange refusal on some later action. A reload swaps
// the whole composition root, so the live read stays honest.
//
// Both higher-tier supervisors are constructed whatever the configured
// tier is. They cost a struct each, and building them lazily would mean a
// tier-2 misconfiguration was discovered by the first person whose action
// was held rather than at boot.
func attachTierSupervision(
	cfg *runtime.Config, attention supervision.AttentionPusher, log audit.Writer,
) (*supervision.TierDispatcher, error) {
	tier := supervision.DefaultSupervisionTier
	if cfg != nil {
		parsed, _, err := supervision.ParseSupervisionConfig(cfg.Extra)
		if err != nil {
			return nil, err
		}
		tier = parsed
	}
	tier2, err := supervision.NewTier2Supervisor(
		supervision.NewPTYAttacher(), os.Getenv, attention, log)
	if err != nil {
		return nil, err
	}
	return supervision.NewTierDispatcher(
		func() supervision.Tier { return tier },
		tier2,
		supervision.NewTier3Supervisor(attention, log),
	)
}
