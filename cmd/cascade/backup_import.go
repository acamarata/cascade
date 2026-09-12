// Purpose: implement elevated portable backup import with optional vault restore.
// Inputs: an artifact path and optional passphrase file.
// Outputs: a verified import report; the artifact infers its own chain tip.
// Constraints: no snapshot-id argument and no unwrapped secret output.
// SPORT: cmd.cascade.backup-import/ADD (P1-E19-W4-S42-T3).
package main

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/pkg/cascade"
)

type backupImportFlags struct {
	inPath         string
	passphraseFile string
	yes            bool
}

type backupImportParams struct {
	Target      string `json:"target"`
	Input       string `json:"input"`
	ImportVault bool   `json:"import_vault"`
}

func newBackupImportCmd(deps backupDeps) *cobra.Command {
	flags := backupImportFlags{}
	cmd := &cobra.Command{
		Use: "import", Short: "Import and verify a tar.zst.age artifact (elevated)", Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error { return runBackupImport(cmd, deps, flags) },
	}
	cmd.Flags().StringVar(&flags.inPath, "in", "", "read the portable artifact from this path")
	cmd.Flags().StringVar(&flags.passphraseFile, "vault-passphrase-file", "", "unlock an included vault export with this file")
	cmd.Flags().BoolVar(&flags.yes, "yes", false, "confirm the elevated operation")
	_ = cmd.MarkFlagRequired("in")
	return cmd
}

func runBackupImport(cmd *cobra.Command, deps backupDeps, flags backupImportFlags) error {
	rt, err := deps.Open(cmd.Context())
	if err != nil {
		return err
	}
	defer rt.Close()
	record, err := selectBackupRecord(cmd.Context(), rt.Store, "")
	if err != nil {
		return err
	}
	params, err := json.Marshal(backupImportParams{
		Target: record.Name, Input: flags.inPath, ImportVault: flags.passphraseFile != "",
	})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "backup: encode import parameters")
	}
	proof, err := deps.Authorize(cmd.Context(), cmd, "backup.import", params, flags.yes)
	if err != nil {
		return err
	}
	return importPortableBackup(cmd, deps, record, flags, proof)
}

func importPortableBackup(cmd *cobra.Command, deps backupDeps, record backup.TargetRecord, flags backupImportFlags, proof backup.ElevationProof) error {
	artifact, err := deps.ReadFile(flags.inPath)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "backup: read portable import")
	}
	target, err := deps.BuildTarget(cmd.Context(), record, nil, deps.Getenv)
	if err != nil {
		return err
	}
	opts, err := backupImportOptions(deps, target, flags.passphraseFile, proof)
	if err != nil {
		return err
	}
	report, err := deps.Import(cmd.Context(), proof, opts, artifact, "")
	if err != nil {
		return err
	}
	return backupOutputWriter(cmd).Result(report)
}

func backupImportOptions(deps backupDeps, target backup.Target, passphraseFile string, proof backup.ElevationProof) (backup.ImportOptions, error) {
	opts := backup.ImportOptions{Dest: target}
	if passphraseFile == "" {
		return opts, nil
	}
	passphrase, err := readBackupPassphrase(deps, passphraseFile)
	if err != nil {
		return backup.ImportOptions{}, err
	}
	broker, err := deps.NewVault(proof)
	if err != nil {
		return backup.ImportOptions{}, err
	}
	opts.VaultImporter = backupVaultAdapter{broker: broker}
	opts.VaultPassphrase = passphrase
	return opts, nil
}
