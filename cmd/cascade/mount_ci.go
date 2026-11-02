// Purpose: registers the `ci` root mount and binds the ci tree's attach
// points: deps.WatchEvents and deps.AttestVerify are set before
// ci.NewCICmd (contract:ci-cli-attach), and `attest verify` gains its
// --require and --repo-id flags (contract:ci-attestation).
// SPORT: cmd/cascade root mounts (P1-CORE-01).
package main

import (
	"context"
	"database/sql"
	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/runtime"
)

var _ = registerRootMount(rootMount{Name: "ci", Order: 280, Mount: mountCIAttachedCmd})

// mountCIAttachedCmd mounts the `ci` tree with its daemon stream and
// attestation verifier bound.
func mountCIAttachedCmd(root *cobra.Command) {
	deps := ci.ProductionCmdDeps()
	deps.WatchEvents = ciWatchEvents(productionStatusDeps())
	deps.AttestVerify = ciAttestVerify(productionCIAttestVerifyDeps())
	deps.RepoRegister = ciRepoRegister(productionStatusDeps(), productionElevatedCallDeps(nil))
	deps.RepoList = ciRepoList
	cmd := ci.NewCICmd(deps)
	addCIAttestVerifyFlags(cmd)
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

func acceptanceNonceRepo(ctx context.Context, db *sql.DB, clock runtime.Clock, nonce, repoID string) error {
	nonces, _ := jobs.NewAttestationNonceStore(db, clock)
	fact, ok, err := nonces.Issued(ctx, nonce)
	if err != nil {
		return err
	}
	if !ok || fact.RepoID != repoID {
		return ci.ErrCheckpointStale
	}
	return nil
}
