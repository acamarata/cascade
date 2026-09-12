// Purpose: implement elevated `backup create [--target] [--yes]`.
// Inputs: one persisted target policy, the runtime database, and a verified
// local elevation attestation.
// Outputs: a signed encrypted snapshot plus outcome bookkeeping.
// Constraints: no target I/O or capture begins before a proof is minted.
// SPORT: cmd.cascade.backup-create/ADD (P1-E19-W4-S42-T3).
package main

import (
	"context"
	"encoding/json"

	"filippo.io/age"
	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

type backupCreateParams struct {
	Target  string   `json:"target"`
	Domains []string `json:"domains"`
}

func newBackupCreateCmd(deps backupDeps) *cobra.Command {
	var targetName string
	var yes bool
	cmd := &cobra.Command{
		Use: "create", Short: "Create an encrypted snapshot (elevated)", Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBackupCreate(cmd, deps, targetName, yes)
		},
	}
	cmd.Flags().StringVar(&targetName, "target", "", "configured target name")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm the elevated operation")
	return cmd
}

func runBackupCreate(cmd *cobra.Command, deps backupDeps, targetName string, yes bool) error {
	rt, err := deps.Open(cmd.Context())
	if err != nil {
		return err
	}
	defer rt.Close()
	record, err := selectBackupRecord(cmd.Context(), rt.Store, targetName)
	if err != nil {
		return err
	}
	policy, err := backup.GetPolicy(cmd.Context(), rt.Store, backupRegistryNamespace, record.Name)
	if err != nil {
		return err
	}
	params, err := json.Marshal(backupCreateParams{Target: record.Name, Domains: policy.Domains})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "backup: encode create parameters")
	}
	proof, err := deps.Authorize(cmd.Context(), cmd, "backup.create", params, yes)
	if err != nil {
		return err
	}
	return createBackupSnapshot(cmd.Context(), cmd, deps, rt, record, policy, proof)
}

func createBackupSnapshot(ctx context.Context, cmd *cobra.Command, deps backupDeps, rt *backupRuntime, record backup.TargetRecord, policy backup.TargetPolicy, proof backup.ElevationProof) error {
	target, err := deps.BuildTarget(ctx, record, nil, deps.Getenv)
	if err != nil {
		return recordCreateFailure(ctx, rt, deps, record.Name, err)
	}
	recipient, err := backupAgeRecipient()
	if err != nil {
		return recordCreateFailure(ctx, rt, deps, record.Name, err)
	}
	previous, err := previousBackupManifest(ctx, rt, record.Name, target)
	if err != nil {
		return recordCreateFailure(ctx, rt, deps, record.Name, err)
	}
	manifest, err := deps.Create(ctx, proof, backup.CreateSnapshotDeps{
		Target: target, AgeRecipient: recipient, Clock: deps.Clock,
		Domains: backupCaptureDomains(rt, policy.Domains),
		Escrow:  backupEscrowChecker(deps, rt),
	}, previous)
	if err != nil {
		return recordCreateFailure(ctx, rt, deps, record.Name, err)
	}
	if err := backup.RecordOutcome(ctx, rt.Store, backupRegistryNamespace, backup.Outcome{
		Target: record.Name, Snapshot: string(manifest.Snapshot), When: deps.Clock.Now(), Success: true,
	}); err != nil {
		return err
	}
	return backupOutputWriter(cmd).Result(manifest)
}

func backupAgeRecipient() (string, error) {
	identityText, err := backup.AgeIdentity()
	if err != nil {
		return "", err
	}
	identity, err := age.ParseX25519Identity(identityText)
	if err != nil {
		return "", cascade.Wrap(cascade.KindInvalidInput, err, "backup: invalid age identity")
	}
	return identity.Recipient().String(), nil
}

func backupCaptureDomains(rt *backupRuntime, names []string) map[string]backup.Exporter {
	out := make(map[string]backup.Exporter, len(names))
	for _, name := range names {
		out[name] = backup.SQLiteCapture{DB: rt.DB, Domain: storage.DomainID(name), Dir: rt.DataDir}
	}
	return out
}

func previousBackupManifest(ctx context.Context, rt *backupRuntime, name string, target backup.Target) (*backup.Manifest, error) {
	rows, err := backup.ListSnapshots(ctx, rt.Store, backupRegistryNamespace,
		[]backup.NamedTarget{{Name: name, Target: target}})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &backup.Manifest{Snapshot: rows[0].ID}, nil
}

func recordCreateFailure(ctx context.Context, rt *backupRuntime, deps backupDeps, target string, cause error) error {
	_ = backup.RecordOutcome(ctx, rt.Store, backupRegistryNamespace, backup.Outcome{
		Target: target, When: deps.Clock.Now(), Success: false, ErrorText: cause.Error(),
	})
	return cause
}
