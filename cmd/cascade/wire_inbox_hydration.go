//go:build !windows

// Purpose: the daemon registration of addressed inbox hydration, shipped
//
//	flag-off. The registration builds the cascade-claude bridge over the
//	daemon store and records inbox.hydration Skipped while
//	inboxHydrationLive is false; the context.slice registration the stall
//	wiring owns (registerContextDrain) appends inboxHydrationOption(w), whose
//	source drains nothing until the flag is flipped. That registration runs only
//	when the stall wiring builds, so the flip also needs the stall wiring (or a
//	registration of this option of its own); the manifest entry here does not
//	report it.
//
// Inputs: the daemon wiring (Store, Clock, Bus, Manifest).
// Outputs: the inbox.hydration manifest entry; the daemon.ContextOption
//
//	registerContextDrain appends.
//
// Constraints: flipping inboxHydrationLive is a task of P1-HRN-08 (the tool
//
//	gate), after P1-HRN-03 derives data classes; until then production
//	delivers nothing and leaves every queue untouched. No goroutine starts
//	here. A construction failure is recorded Failed and the daemon boots
//	with a source that drains nothing (a missing hydration is missing
//	context, never a refused boot). This file does not import internal/rpc.
//
// SPORT: cmd/cascade daemon registrations (P1-HRN-02).
package main

import "github.com/acamarata/cascade/internal/daemon"

// inboxHydrationLive gates delivery. P1-HRN-08 flips it.
const inboxHydrationLive = false

// inboxHydrationSubsystem is the manifest name and the store namespace.
const inboxHydrationSubsystem = "inbox.hydration"

var _ = registerDaemonWiring(daemonRegistration{
	Name: "inbox-hydration", Phase: phaseNamespaces, Order: 30,
	Wire: wireInboxHydration,
})

// wireInboxHydration is the inbox-hydration registration: validate that the
// bridge builds over the daemon store and record the flag-off state.
func wireInboxHydration(w *daemonWiring) error {
	switch {
	case w.Store == nil:
		w.Manifest.Skipped(inboxHydrationSubsystem, "no daemon store")
	case !inboxHydrationLive:
		if _, err := newInboxBridge(w.Ctx, w); err != nil {
			w.Manifest.Failed(inboxHydrationSubsystem, err.Error())
			return nil
		}
		w.Manifest.Skipped(inboxHydrationSubsystem, "disabled until the tool gate ships (P1-HRN-08)")
	default:
		if _, err := newInboxBridge(w.Ctx, w); err != nil {
			w.Manifest.Failed(inboxHydrationSubsystem, err.Error())
		}
	}
	return nil
}

// inboxHydrationOption is the context.slice option for w: the live-session
// gated bridge drain once inboxHydrationLive is true, otherwise (and on any
// construction failure) a source that drains nothing.
func inboxHydrationOption(w *daemonWiring) daemon.ContextOption {
	if !inboxHydrationLive || w.Store == nil {
		return daemon.WithInboxHydration(nil)
	}
	src, err := newBridgeInboxSource(w.Ctx, w)
	if err != nil {
		return daemon.WithInboxHydration(nil)
	}
	return daemon.WithInboxHydration(src)
}
