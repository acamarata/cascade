//go:build !windows

// Purpose: registers context.facts.list, context.facts.propose and
// context.facts.accept on the daemon registry (context_facts_rpc.go), over
// the daemon's paths and clock, the process's in-process conductor door as
// the one model executor, the repository admission guard and git's tracked
// file list.
// Inputs: w.Paths and w.Clock.
// Outputs: three registered methods.
// Constraints: no database is opened at wiring time, so a test-built
// registry wires cleanly; the model executor is only ever the door
// (daemon.ProcessConductorDoor), so an unbound door refuses propose with
// KindUnavailable before anything is written.
// SPORT: cmd/cascade context facts wiring (P1-GEN-02).
package main

import (
	"context"
	"os/exec"
	"strings"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/repo"
	"github.com/acamarata/cascade/pkg/cascade"
)

var _ = registerDaemonWiring(daemonRegistration{
	Name: "context-facts", Phase: phaseLate, Order: 50,
	Wire: func(w *daemonWiring) error {
		mountContextFacts(w.Registry, productionContextFactsDeps(w))
		return nil
	},
})

// productionContextFactsDeps builds the handlers' collaborators for w.
func productionContextFactsDeps(w *daemonWiring) contextFactsDeps {
	return contextFactsDeps{
		Paths:   w.Paths,
		Clock:   w.Clock,
		Exec:    daemon.ProcessConductorDoor(),
		Admit:   repo.AdmitBatch,
		Tracked: gitTrackedFiles,
	}
}

// gitTrackedFiles lists root's tracked paths (slash form).
func gitTrackedFiles(ctx context.Context, root string) ([]string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "context.facts.propose: git ls-files")
	}
	var rels []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			rels = append(rels, p)
		}
	}
	return rels, nil
}
