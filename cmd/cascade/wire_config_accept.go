//go:build !windows

// Purpose: registers config.accept_loosening on the daemon registry, served
// by the boot baseline's handler. The verb is always elevated (elevation
// table row) and owner-only (declared in internal/rpc/attestation_record.go).
// Inputs: w.Deps.ConfigBaseline (set by composeDaemon).
// Outputs: one registered method.
// Constraints: a wiring without a baseline (a test-built registry) registers
// a handler that refuses, so the verb never answers as if it had applied.
// SPORT: cmd/cascade config.accept_loosening wiring (P1-SEC-04).
package main

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

var _ = registerDaemonWiring(daemonRegistration{
	Name: "config-accept-loosening", Phase: phaseCore, Order: 25,
	Wire: func(w *daemonWiring) error {
		b := w.Deps.ConfigBaseline
		if b == nil {
			w.Registry.Register(runtime.AcceptLooseningMethod, func(context.Context, json.RawMessage) (any, error) {
				return nil, cascade.New(cascade.KindUnavailable, "config.accept_loosening: this daemon has no config baseline")
			})
			return nil
		}
		w.Registry.Register(runtime.AcceptLooseningMethod, b.acceptLoosening)
		return nil
	},
})
