//go:build !windows

// Purpose: registers the daemon's audit.clipboard_effect verb over the
//
//	daemon's one audit log, so a `vault get --clipboard` CLI writes its
//	effect and refusal records there while the daemon holds the store.
//
// Inputs: the daemon wiring's store, clock and paths.
// Outputs: the "audit-clipboard-effect" registration; the owner-only access
//
//	declaration.
//
// Constraints: owner-only; the log is the daemon log openDaemonAuditLog
//
//	opened and its memoised effect log; no audit.New.
//
// SPORT: cmd/cascade daemon registrations — audit.clipboard_effect (P1-SEC-09, P1-BF-R298).
package main

import "github.com/acamarata/cascade/internal/rpc"

var _ = rpc.DeclareAccess(clipboardEffectMethod, rpc.AccessOwnerOnly)

var _ = registerDaemonWiring(daemonRegistration{
	Name: "audit-clipboard-effect", Phase: phaseNamespaces, Order: 44,
	Wire: func(w *daemonWiring) error {
		if w.Store == nil {
			return nil
		}
		log, err := openDaemonAuditLog(w.Store, w.Clock, nil, w.Paths)
		if err != nil {
			return err
		}
		fx, err := openEffectLog(w.Store, w.Clock, nil, w.Paths)
		if err != nil {
			return err
		}
		w.Registry.Register(clipboardEffectMethod, clipboardEffectHandler(log, fx, w.Clock))
		return nil
	},
})
