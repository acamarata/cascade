//go:build !windows

// Purpose: the daemon's ONE metrics registry, and the fleet counters
//
//	registered against it (P1-E18-W4-S40-T3).
//
// WHY A PACKAGE-LEVEL VALUE, STATED PLAINLY. Two composition paths need
//
//	the SAME registry and they run at different times: wirePolicy builds
//	the supervision stages that COUNT, and buildRPCServer later mounts the
//	supervisor.snapshot handler that READS. Threading one value between
//	them means a positional parameter on buildRPCServer, which has a dozen
//	test call sites — the signature ripple the rpcServerOption mechanism
//	was introduced to avoid (R-16.79, "smallest real change"). The
//	alternative — a registry per path — is worse than a global: the
//	snapshot would report zeros while the counters climbed, and nothing
//	would say which one was lying.
//
//	It is written exactly once, at boot, by the composition root, and read
//	afterwards. That is the same shape globalFlags already has in this
//	package. It is NOT a general-purpose global: nothing outside this file
//	mutates it, and daemonMetrics returns a usable registry even if the
//	policy path never ran, so a caller never has to nil-check it.
//
// Constraints: registration is once-only — runtime.Registry.RegisterCounter
//
//	panics on a duplicate name by design — so the registry is created
//	lazily under a sync.Once and the fleet counters are built with it.
//
// SPORT: cmd/cascade/daemon (CHANGED — fleet metrics wiring).

package main

import (
	"context"
	"log/slog"
	"sync"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet"
	"github.com/acamarata/cascade/internal/runtime"
)

var (
	daemonMetricsOnce  sync.Once
	daemonMetricsReg   *runtime.Registry
	daemonFleetMetrics *fleet.Metrics
	daemonMetricsErr   error
)

// daemonMetrics returns the process's one metrics registry and the fleet
// counter set registered against it.
//
// A construction failure is returned rather than swallowed: a daemon whose
// counters could not be registered would show zeros forever, which reads
// exactly like a quiet fleet.
func daemonMetrics() (*runtime.Registry, *fleet.Metrics, error) {
	daemonMetricsOnce.Do(func() {
		daemonMetricsReg = runtime.NewRegistry()
		daemonFleetMetrics, daemonMetricsErr = fleet.NewMetrics(daemonMetricsReg)
	})
	return daemonMetricsReg, daemonFleetMetrics, daemonMetricsErr
}

// startFleetMetricsConsumer opens the attention subscription the fleet
// counters read and runs it until ctx ends.
//
// A subscription that cannot be opened is LOGGED and skipped, not fatal.
// The counters are observability: refusing to start a daemon because one
// of its graphs would be incomplete trades a working system for a tidier
// dashboard. The log line is what makes the gap visible rather than
// silent.
func startFleetMetricsConsumer(ctx context.Context, bus *events.Bus, logger *slog.Logger) {
	_, metrics, err := daemonMetrics()
	if err != nil {
		logger.Error("fleet metrics unavailable", "error", err)
		return
	}
	if bus == nil {
		return
	}
	sub, err := bus.Subscribe(ctx, fleet.AttentionNamespace, fleetMetricsCursor, fleetMetricsBuffer)
	if err != nil {
		logger.Error("fleet metrics: attention subscription unavailable", "error", err)
		return
	}
	go func() {
		defer func() { _ = sub.Unsubscribe() }()
		if consumeErr := metrics.ConsumeAttention(ctx, sub); consumeErr != nil {
			logger.Error("fleet metrics: attention stream ended", "error", consumeErr)
		}
	}()
}

// fleetMetricsCursor is the durable subscription cursor this consumer
// commits under, distinct from every other subscriber's — Bus.Subscribe
// refuses two live subscriptions sharing one cursor outright.
const fleetMetricsCursor = "fleet-metrics"

// fleetMetricsBuffer bounds how far this subscriber may fall behind before
// its delivery goroutine blocks. Counting is O(1) per event, so the buffer
// only has to absorb a burst, not a slow consumer.
const fleetMetricsBuffer = 256
