// Purpose: implement gate-first elevated `backup restore [--domain] [--yes]`.
// Inputs: the single configured target, its latest snapshot, and selected domains.
// Outputs: the owning restore operation's report.
// Constraints: no positional snapshot id and no restore work before elevation.
// SPORT: cmd.cascade.backup-restore/ADD (P1-E19-W4-S42-T3).
package main

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/pkg/cascade"
)

type backupRestoreParams struct {
	Target  string   `json:"target"`
	Domains []string `json:"domains,omitempty"`
}

func newBackupRestoreCmd(deps backupDeps) *cobra.Command {
	var domains []string
	var yes bool
	cmd := &cobra.Command{
		Use: "restore", Short: "Restore the latest snapshot (elevated)", Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(domains) > 0 {
				if err := validateBackupDomains(domains); err != nil {
					return err
				}
			}
			return runBackupRestore(cmd, deps, domains, yes)
		},
	}
	cmd.Flags().StringSliceVar(&domains, "domain", nil, "restore only this domain (repeatable)")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm the elevated operation")
	return cmd
}

func runBackupRestore(cmd *cobra.Command, deps backupDeps, domains []string, yes bool) error {
	rt, err := deps.Open(cmd.Context())
	if err != nil {
		return err
	}
	defer rt.Close()
	record, err := selectBackupRecord(cmd.Context(), rt.Store, "")
	if err != nil {
		return err
	}
	params, err := json.Marshal(backupRestoreParams{Target: record.Name, Domains: domains})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "backup: encode restore parameters")
	}
	proof, err := deps.Authorize(cmd.Context(), cmd, "backup.restore", params, yes)
	if err != nil {
		return err
	}
	target, err := deps.BuildTarget(cmd.Context(), record, nil, deps.Getenv)
	if err != nil {
		return err
	}
	return restoreLatestBackup(cmd, deps, rt, record.Name, target, domains, proof)
}

func restoreLatestBackup(cmd *cobra.Command, deps backupDeps, rt *backupRuntime, targetName string, target backup.Target, domains []string, proof backup.ElevationProof) error {
	rows, err := backup.ListSnapshots(cmd.Context(), rt.Store, backupRegistryNamespace,
		[]backup.NamedTarget{{Name: targetName, Target: target}})
	if err != nil {
		return err
	}
	latest, err := latestSnapshot(rows, targetName)
	if err != nil {
		return err
	}
	config, err := backup.ReadRepoConfig(cmd.Context(), target)
	if err != nil {
		return err
	}
	pubKey, err := decodeBackupPubKey(config.ManifestSigningPubKey)
	if err != nil {
		return err
	}
	report, err := deps.Restore(cmd.Context(), proof, backup.RestoreOptions{
		Target: target, DB: rt.DB, PubKey: pubKey, Domains: domains,
	}, latest.ID)
	if err != nil {
		return err
	}
	return backupOutputWriter(cmd).Result(report)
}
