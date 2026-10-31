//go:build !windows

package main

import (
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/pkg/cascade"
	"net/http"
)

var _ = registerDaemonWiring(daemonRegistration{
	Name: "plugin-host-boundary", Phase: phaseCore, Order: 17,
	Wire: func(w *daemonWiring) error {
		if !hostBoundaryInstalled {
			if w.Manifest != nil {
				w.Manifest.Failed("plugin-host-boundary", "factory unavailable")
			}
			return cascade.ErrUnavailable
		}
		plugins.SetHostBoundary(newHostBoundaryFactory(w.Paths, http.DefaultTransport))
		if w.Manifest != nil {
			w.Manifest.Started("plugin-host-boundary", "host calls enforced")
		}
		return nil
	},
})
