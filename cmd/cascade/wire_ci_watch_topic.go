//go:build !windows

// Purpose: registers the ci-watch-topic daemon wiring: the owner-only `ci`
// topic on GET /events that `cascade ci watch` reads, and its replay-end method
// (P1-CI-11, P1-BF-R298 and the T0 individual ruling).
// SPORT: cmd/cascade daemon registrations (P1-CORE-01).
package main

import "github.com/acamarata/cascade/internal/ci"

var _ = registerDaemonWiring(daemonRegistration{
	Name: "ci-watch-topic", Phase: phaseEventTopics, Order: 30,
	Wire: func(w *daemonWiring) error {
		w.Events.WithTopic(ci.WatchTopic, ci.NewWatchSSEHandler(w.Bus, w.Clock))
		w.Registry.Register(ci.MethodWatchReplayEnd, ci.WatchReplayEndHandler(w.Bus))
		return nil
	},
})
