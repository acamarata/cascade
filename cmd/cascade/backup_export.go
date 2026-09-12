// Purpose: implement elevated portable backup export with opt-in vault data.
// Inputs: the latest snapshot, an output path, and optional passphrase file.
// Outputs: one mode-0600 tar.zst.age artifact and non-secret metadata.
// Constraints: vault export is default-off and no passphrase/value is printed.
// SPORT: cmd.cascade.backup-export/ADD (P1-E19-W4-S42-T3).
package main

import (
	"encoding/json"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/pkg/cascade"
)

type backupExportFlags struct {
	outPath        string
	includeVault   bool
	passphraseFile string
	yes            bool
}

type backupExportParams struct {
	Target       string `json:"target"`
	Output       string `json:"output"`
	IncludeVault bool   `json:"include_vault"`
}

func newBackupExportCmd(deps backupDeps) *cobra.Command {
	flags := backupExportFlags{}
	cmd := &cobra.Command{
		Use: "export", Short: "Export the latest snapshot as tar.zst.age (elevated)", Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error { return runBackupExport(cmd, deps, flags) },
	}
	cmd.Flags().StringVar(&flags.outPath, "out", "", "write the portable artifact to this path")
	cmd.Flags().BoolVar(&flags.includeVault, "include-vault", false, "opt in to passphrase-wrapped vault export")
	cmd.Flags().StringVar(&flags.passphraseFile, "vault-passphrase-file", "", "read the vault-wrap passphrase from this file")
	cmd.Flags().BoolVar(&flags.yes, "yes", false, "confirm the elevated operation")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func runBackupExport(cmd *cobra.Command, deps backupDeps, flags backupExportFlags) error {
	if flags.passphraseFile != "" && !flags.includeVault {
		return cascade.New(cascade.KindInvalidInput,
			"backup: --vault-passphrase-file requires --include-vault")
	}
	rt, err := deps.Open(cmd.Context())
	if err != nil {
		return err
	}
	defer rt.Close()
	record, err := selectBackupRecord(cmd.Context(), rt.Store, "")
	if err != nil {
		return err
	}
	params, err := json.Marshal(backupExportParams{
		Target: record.Name, Output: flags.outPath, IncludeVault: flags.includeVault,
	})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "backup: encode export parameters")
	}
	proof, err := deps.Authorize(cmd.Context(), cmd, "backup.export", params, flags.yes)
	if err != nil {
		return err
	}
	return exportLatestBackup(cmd, deps, rt, record, flags, proof)
}

func exportLatestBackup(cmd *cobra.Command, deps backupDeps, rt *backupRuntime, record backup.TargetRecord, flags backupExportFlags, proof backup.ElevationProof) error {
	target, err := deps.BuildTarget(cmd.Context(), record, nil, deps.Getenv)
	if err != nil {
		return err
	}
	rows, err := backup.ListSnapshots(cmd.Context(), rt.Store, backupRegistryNamespace,
		[]backup.NamedTarget{{Name: record.Name, Target: target}})
	if err != nil {
		return err
	}
	latest, err := latestSnapshot(rows, record.Name)
	if err != nil {
		return err
	}
	opts, err := backupExportOptions(cmd, deps, target, flags, proof)
	if err != nil {
		return err
	}
	artifact, err := deps.Export(cmd.Context(), proof, opts, latest.ID)
	if err != nil {
		return err
	}
	if err := deps.WriteFile(flags.outPath, artifact, 0o600); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "backup: write portable export")
	}
	return backupOutputWriter(cmd).Result(map[string]any{
		"snapshot": latest.ID, "target": record.Name, "output": flags.outPath,
		"vault_included": flags.includeVault,
	})
}

func backupExportOptions(_ *cobra.Command, deps backupDeps, target backup.Target, flags backupExportFlags, proof backup.ElevationProof) (backup.ExportOptions, error) {
	opts := backup.ExportOptions{Target: target, IncludeVault: flags.includeVault}
	if !flags.includeVault {
		return opts, nil
	}
	passphrase, err := readBackupPassphrase(deps, flags.passphraseFile)
	if err != nil {
		return backup.ExportOptions{}, err
	}
	broker, err := deps.NewVault(proof)
	if err != nil {
		return backup.ExportOptions{}, err
	}
	opts.VaultBroker = backupVaultAdapter{broker: broker}
	opts.VaultPassphrase = passphrase
	return opts, nil
}

func readBackupPassphrase(deps backupDeps, path string) (string, error) {
	if path == "" {
		return "", cascade.New(cascade.KindInvalidInput,
			"backup: opt-in vault handling requires --vault-passphrase-file")
	}
	data, err := deps.ReadFile(path)
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "backup: read vault passphrase file")
	}
	passphrase := strings.TrimRight(string(data), "\r\n")
	for i := range data {
		data[i] = 0
	}
	if passphrase == "" {
		return "", cascade.New(cascade.KindInvalidInput, "backup: vault passphrase file is empty")
	}
	return passphrase, nil
}
