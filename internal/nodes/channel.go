// Purpose: the §D-33 install-channel deferral gate. A binary stamped
//   install_channel="node-managed" (internal/buildinfo.InstallChannel) is
//   under the controller's coordinated rollout, not a self-directed
//   update: AA/S-55.T7's `self-update` reads DeferSelfUpdate to decide
//   whether it may act at all.
// Inputs: the resolved §D-33 install channel string (buildinfo's own
//   ResolvedInstallChannel, already normalized to the five-value set).
// Outputs: a bool the caller (this ticket's `node upgrade` rollout, or
//   AA/S-55.T7's self-update) branches on.
// Constraints: this is a pure, total function over buildinfo's closed
//   ValidInstallChannels set — it never reads buildinfo package state
//   directly (buildinfo.InstallChannel is a package var; passing the
//   resolved string in keeps this file's functions unit-testable without
//   mutating a shared global, Art.7.1).
// SPORT: internal/nodes DeferSelfUpdate/ADDED (P1-E17-W4-S36-T5).

package nodes

import "github.com/acamarata/cascade/pkg/cascade"

// installChannelNodeManaged is the §D-33 channel value a node-managed
// install stamps, mirroring internal/buildinfo.ValidInstallChannels'
// "node-managed" entry (duplicated as a constant, not imported, because
// buildinfo intentionally carries no business logic — see its own
// package doc's Constraints paragraph).
const installChannelNodeManaged = "node-managed"

// DeferSelfUpdate reports whether resolvedChannel names a node-managed
// install: true means the binary was provisioned by a fleet controller's
// enroll/upgrade flow, and any self-directed update path (AA/S-55.T7's
// `self-update`) must defer to the controller's coordinated rollout
// rather than acting unilaterally. Every other resolved channel value
// (script, brew, oci, manual) returns false — those installs own their
// own update path.
func DeferSelfUpdate(resolvedChannel string) bool {
	return resolvedChannel == installChannelNodeManaged
}

// ChannelDeferralError (KindPolicyDenied) is the actionable refusal a
// self-update path returns when DeferSelfUpdate reports true. Exported
// so AA/S-55.T7's `self-update` returns the exact same typed error this
// ticket defines, rather than inventing a second copy.
func ChannelDeferralError() error {
	return cascade.New(cascade.KindPolicyDenied, "nodes: this binary was installed by a fleet controller "+
		"(install_channel=node-managed); use `cascade node upgrade` from the controller, not self-update")
}
