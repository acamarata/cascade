// Purpose: registers the `ci` root mount and binds the ci tree's attach
// points: deps.WatchEvents and deps.AttestVerify are set before
// ci.NewCICmd (contract:ci-cli-attach), and `attest verify` gains its
// --require and --repo-id flags (contract:ci-attestation).
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

import (
	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/ci"
)

var _ = registerRootMount(rootMount{Name: "ci", Order: 280, Mount: mountCIAttachedCmd})

// mountCIAttachedCmd mounts the `ci` tree with its daemon stream and
// attestation verifier bound.
func mountCIAttachedCmd(root *cobra.Command) {
	deps := ci.ProductionCmdDeps()
	deps.WatchEvents = ciWatchEvents(productionStatusDeps())
	deps.AttestVerify = ciAttestVerify(productionCIAttestVerifyDeps())
	cmd := ci.NewCICmd(deps)
	addCIAttestVerifyFlags(cmd)
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}
