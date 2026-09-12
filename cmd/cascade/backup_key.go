// Purpose: implement the S-42.T6 recovery-key ceremony's two elevated
// verbs, `cascade backup key export` and `cascade backup key import
// <path>` -- the age identity (master backup encryption key) and the
// R-14.58 manifest-signing key, both vault-held, with the passphrase-
// wrapped printable armored export/import and the location invariant that
// runs BEFORE elevation.
// Inputs: an output/artifact path, a passphrase (prompted or
// --passphrase-env), and the injected backupDeps.
// Outputs: one printable age file (export), or a vault-loaded identity on
// the restoring machine (import), plus escrow bookkeeping.
// Constraints: the location invariant runs before any attestation attempt;
// CASCADE_NO_INPUT=1 without --passphrase-env never prompts.
// SPORT: cmd.cascade.backup-key/ADD (P1-E19-W4-S42-T6).
package main

import (
	"encoding/base64"
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/pkg/cascade"
)

func newBackupKeyCmd(deps backupDeps) *cobra.Command {
	cmd := &cobra.Command{Use: "key", Short: "Run the recovery-key ceremony (elevated)"}
	cmd.AddCommand(newBackupKeyExportCmd(deps), newBackupKeyImportCmd(deps))
	return cmd
}

type backupKeyExportFlags struct {
	outPath       string
	passphraseEnv string
	yes           bool
}

type backupKeyExportParams struct {
	Output string `json:"output"`
}

func newBackupKeyExportCmd(deps backupDeps) *cobra.Command {
	flags := backupKeyExportFlags{}
	cmd := &cobra.Command{
		Use: "export", Short: "Export the passphrase-wrapped recovery key (elevated)", Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error { return runBackupKeyExport(cmd, deps, flags) },
	}
	cmd.Flags().StringVar(&flags.outPath, "out", "", "write the recovery key artifact to this path")
	cmd.Flags().StringVar(&flags.passphraseEnv, "passphrase-env", "", "read the passphrase from this env var (non-interactive)")
	cmd.Flags().BoolVar(&flags.yes, "yes", false, "confirm the elevated operation")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func runBackupKeyExport(cmd *cobra.Command, deps backupDeps, flags backupKeyExportFlags) error {
	rt, err := deps.Open(cmd.Context())
	if err != nil {
		return err
	}
	defer rt.Close()
	// Location invariant runs BEFORE the elevation flow (the ceremony's
	// own AC): a refusal here never even attempts an attestation.
	targetsList, err := backup.ListTargets(cmd.Context(), rt.Store, backupRegistryNamespace)
	if err != nil {
		return err
	}
	if err := backup.CheckRecoveryKeyLocation(flags.outPath, targetsList); err != nil {
		return err
	}
	params, err := json.Marshal(backupKeyExportParams{Output: flags.outPath})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "backup: encode key export parameters")
	}
	proof, err := deps.Authorize(cmd.Context(), cmd, "backup.key_export", params, flags.yes)
	if err != nil {
		return err
	}
	passphrase, err := resolveKeyPassphrase(cmd, deps, flags.passphraseEnv)
	if err != nil {
		return err
	}
	return exportRecoveryKey(cmd, deps, rt, flags, passphrase, proof)
}

func exportRecoveryKey(cmd *cobra.Command, deps backupDeps, rt *backupRuntime, flags backupKeyExportFlags, passphrase string, proof backup.ElevationProof) error {
	broker, err := deps.NewVault(proof)
	if err != nil {
		return err
	}
	vault := backupVaultStoreAdapter{broker: broker}
	identity, err := backup.EnsureAgeIdentity(cmd.Context(), vault)
	if err != nil {
		return err
	}
	signingPub, err := backup.EnsureManifestSigningKey(cmd.Context(), vault)
	if err != nil {
		return err
	}
	artifact, err := backup.WrapRecoveryKey(passphrase, identity)
	if err != nil {
		return err
	}
	if err := deps.WriteFile(flags.outPath, artifact, 0o600); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "backup: write recovery key artifact")
	}
	if deps.AuditLog != nil {
		if err := backup.RecordKeyEscrowed(cmd.Context(), deps.AuditLog(rt.Store), "backup"); err != nil {
			return err
		}
	}
	guidance := "Recovery key exported and escrowed. Store this file somewhere OTHER than your backup " +
		"target -- it is the only way to decrypt your backups if the vault is lost. Run " +
		"`cascade backup key import " + flags.outPath + "` on a restoring machine to load it back."
	return backupOutputWriter(cmd).Result(map[string]any{
		"output": flags.outPath, "manifest_signing_pubkey": encodeBackupPubKey(signingPub), "guidance": guidance,
	})
}

type backupKeyImportFlags struct {
	passphraseEnv string
	yes           bool
}

type backupKeyImportParams struct {
	Input string `json:"input"`
}

func newBackupKeyImportCmd(deps backupDeps) *cobra.Command {
	flags := backupKeyImportFlags{}
	cmd := &cobra.Command{
		Use: "import <path>", Short: "Import a recovery key artifact (elevated)", Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error { return runBackupKeyImport(cmd, deps, flags, args[0]) },
	}
	cmd.Flags().StringVar(&flags.passphraseEnv, "passphrase-env", "", "read the passphrase from this env var (non-interactive)")
	cmd.Flags().BoolVar(&flags.yes, "yes", false, "confirm the elevated operation")
	return cmd
}

func runBackupKeyImport(cmd *cobra.Command, deps backupDeps, flags backupKeyImportFlags, path string) error {
	artifact, err := deps.ReadFile(path)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "backup: read recovery key artifact")
	}
	params, err := json.Marshal(backupKeyImportParams{Input: path})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "backup: encode key import parameters")
	}
	proof, err := deps.Authorize(cmd.Context(), cmd, "backup.key_import", params, flags.yes)
	if err != nil {
		return err
	}
	passphrase, err := resolveKeyPassphrase(cmd, deps, flags.passphraseEnv)
	if err != nil {
		return err
	}
	identity, err := backup.UnwrapRecoveryKey(passphrase, artifact)
	if err != nil {
		return err
	}
	broker, err := deps.NewVault(proof)
	if err != nil {
		return err
	}
	if err := (backupVaultStoreAdapter{broker: broker}).Set(cmd.Context(), backup.AgeIdentityVaultName, []byte(identity)); err != nil {
		return err
	}
	return backupOutputWriter(cmd).Result(map[string]any{"input": path, "loaded": true})
}

// resolveKeyPassphrase implements §5.8 parity: --passphrase-env is the
// non-interactive path; CASCADE_NO_INPUT=1 without it is a structured
// refusal, never a prompt.
func resolveKeyPassphrase(cmd *cobra.Command, deps backupDeps, envVar string) (string, error) {
	if envVar != "" {
		var value string
		if deps.Getenv != nil {
			value = deps.Getenv(envVar)
		}
		if value == "" {
			return "", cascade.Newf(cascade.KindInvalidInput, "backup: %s is empty or unset", envVar)
		}
		return value, nil
	}
	if deps.Getenv != nil && deps.Getenv("CASCADE_NO_INPUT") == "1" {
		return "", cascade.New(cascade.KindElevationRequired,
			"backup: CASCADE_NO_INPUT=1 requires --passphrase-env; no prompt was attempted")
	}
	if _, err := cmd.ErrOrStderr().Write([]byte("Enter recovery key passphrase: ")); err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "backup: write passphrase prompt")
	}
	line := make([]byte, 512)
	n, _ := cmd.InOrStdin().Read(line)
	passphrase := trimEOL(line[:n])
	if passphrase == "" {
		return "", cascade.New(cascade.KindInvalidInput, "backup: no passphrase was entered")
	}
	return passphrase, nil
}

func trimEOL(b []byte) string {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return string(b)
}

func encodeBackupPubKey(pub []byte) string {
	return base64.StdEncoding.EncodeToString(pub)
}
