//go:build !windows

package main

var _ = registerDaemonWiring(daemonRegistration{
	Name: "jobs-evidence", Phase: phaseFleet, Order: 6,
	Wire: func(w *daemonWiring) error {
		if w.Deps.Evidence == nil {
			w.Manifest.Skipped("jobs-evidence", "no evidence ledger")
			return nil
		}
		h := jobsEvidenceList(w.Deps.Evidence)
		w.Registry.Register("jobs.evidence.list", h)
		w.Registry.Register("plugin.pbd.evidence", h)
		return nil
	},
})

// Order 30 is occupied by ci-watch-topic; 31 is the next free slot.
var _ = registerDaemonWiring(daemonRegistration{
	Name: "jobs-evidence-topic", Phase: phaseEventTopics, Order: 31,
	Wire: func(w *daemonWiring) error {
		if w.Deps.Evidence == nil {
			w.Manifest.Skipped("jobs-evidence-topic", "no evidence ledger")
			return nil
		}
		w.Events.WithTopic("jobs.evidence", &jobsEvidenceSSE{bus: w.Bus})
		return nil
	},
})
