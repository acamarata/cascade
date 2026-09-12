// Purpose: compose and mount the S-42.T3 backup command slice, extended by
// S-42.T4 with the read-only `verify` verb.
// Inputs: injected storage, elevation, target, file, vault, and (S-42.T4)
// verification/attention dependencies.
// Outputs: one flat Cobra command tree rooted at `backup`.
// Constraints: no status, key, profile, dry-run, or snapshot-id verb is
// introduced beyond what S-42.T3/T4/T6 already add.
// SPORT: cmd.cascade.backup/ADD (P1-E19-W4-S42-T3); CHANGED (P1-E19-W4-S42-T4).
package main

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"os"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

const backupRegistryNamespace = "daemon-scheduler"

type backupRuntime struct {
	Store   provider.Store
	DB      *sql.DB
	DataDir string
	Close   func()
}

type backupAuthorizeFunc func(context.Context, *cobra.Command, string, []byte, bool) (backup.ElevationProof, error)

type backupDeps struct {
	Open        func(context.Context) (*backupRuntime, error)
	BuildTarget func(context.Context, backup.TargetRecord, *egress.Engine, func(string) string) (backup.Target, error)
	Authorize   backupAuthorizeFunc
	Clock       runtime.Clock
	Getenv      runtime.Getenv
	ReadFile    func(string) ([]byte, error)
	WriteFile   func(string, []byte, os.FileMode) error
	NewVault    func(backup.ElevationProof) (*secrets.Broker, error)
	Create      func(context.Context, backup.ElevationProof, backup.CreateSnapshotDeps, *backup.Manifest) (backup.Manifest, error)
	Restore     func(context.Context, backup.ElevationProof, backup.RestoreOptions, backup.SnapshotID) (backup.RestoreReport, error)
	Export      func(context.Context, backup.ElevationProof, backup.ExportOptions, backup.SnapshotID) ([]byte, error)
	Import      func(context.Context, backup.ElevationProof, backup.ImportOptions, []byte, backup.SnapshotID) (backup.ImportReport, error)
	// AuditLog builds the S-42.T6 recovery-key ceremony's audit-domain
	// collaborator over store: RecordKeyEscrowed (backup_key.go) writes
	// through it, and AuditEscrowChecker (backup_create.go) reads through
	// it. Nil in a caller that predates the ceremony is a documented
	// no-op: backupEscrowChecker below never dereferences a nil AuditLog.
	AuditLog func(provider.Store) *audit.Log
	// Verify runs S-42.T4's read-only verification pass -- the exact
	// RunVerification the scheduled cron calls (06 §5.8 automation
	// parity). Assigned directly to backup.RunVerification in production.
	Verify func(context.Context, provider.Store, string, backup.TargetRecord, *egress.Engine, func(string) string, runtime.Clock, backup.AttentionSink) (backup.VerificationReport, error)
	// NewAttentionSink builds the real R/S-39.T1 attention sink a verify
	// failure is routed to, over the SAME store `backup verify` already
	// opened. No SSE bus in a one-shot CLI process (nil -- NewStore's
	// documented no-op mode), matching internal/daemon/attention_rpc.go's
	// own nil-bus precedent for the identical Store constructor.
	NewAttentionSink func(provider.Store) backup.AttentionSink
}

func productionBackupDeps() backupDeps {
	paths := lazyPaths{}
	clock := runtime.NewSystemClock()
	vaultDeps := productionVaultDeps()
	return backupDeps{
		Open: func(ctx context.Context) (*backupRuntime, error) {
			return openBackupRuntime(ctx, paths, clock)
		},
		BuildTarget: backup.BuildTarget,
		Authorize:   newBackupAuthorizer(productionElevateHelperDeps()),
		Clock:       clock,
		Getenv:      os.Getenv,
		ReadFile:    os.ReadFile,
		WriteFile:   os.WriteFile,
		NewVault: func(proof backup.ElevationProof) (*secrets.Broker, error) {
			custody, err := vaultDeps.NewCustody()
			if err != nil {
				return nil, err
			}
			return secrets.NewBroker(custody, backupProofGate{proof: proof})
		},
		Create:  backup.CreateSnapshot,
		Restore: backup.Restore,
		Export:  backup.ExportPortable,
		Import:  backup.ImportPortable,
		AuditLog: func(store provider.Store) *audit.Log {
			return audit.New(store, clock, nil)
		},
		Verify: backup.RunVerification,
		NewAttentionSink: func(store provider.Store) backup.AttentionSink {
			return supervision.NewStore(store, clock, nil, supervision.NewSystemIDGenerator(), 0)
		},
	}
}

// backupEscrowChecker builds the S-42.T6 escrow guard over rt's store, or
// nil when deps.AuditLog is unset -- the documented no-op default every
// pre-ticket backupDeps literal (test scaffolding included) already relies
// on, since CreateSnapshotDeps.Escrow itself tolerates nil (snapshot.go).
func backupEscrowChecker(deps backupDeps, rt *backupRuntime) backup.EscrowChecker {
	if deps.AuditLog == nil {
		return nil
	}
	return backup.AuditEscrowChecker{Reader: deps.AuditLog(rt.Store)}
}

func mountBackupCmd(root *cobra.Command) {
	cmd := newBackupCmd(productionBackupDeps())
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

func newBackupCmd(deps backupDeps) *cobra.Command {
	cmd := &cobra.Command{Use: "backup", Short: "Create, list, restore, export, and import encrypted backups"}
	cmd.AddCommand(
		newBackupListCmd(deps), newBackupTargetCmd(deps), newBackupCreateCmd(deps),
		newBackupRestoreCmd(deps), newBackupExportCmd(deps), newBackupImportCmd(deps),
		newBackupKeyCmd(deps), newBackupVerifyCmd(deps),
	)
	return cmd
}

func backupOutputWriter(cmd *cobra.Command) *output.Writer {
	jsonOut, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	verbose, _ := cmd.Flags().GetBool("verbose")
	noColor, _ := cmd.Flags().GetBool("no-color")
	return output.New(cmd.OutOrStdout(), cmd.OutOrStderr(), jsonOut, quiet, verbose, noColor)
}

func decodeBackupPubKey(encoded string) (ed25519.PublicKey, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, cascade.New(cascade.KindIntegrity, "backup: repository manifest public key is invalid")
	}
	return ed25519.PublicKey(key), nil
}

type backupProofGate struct{ proof backup.ElevationProof }

// Authorize allows every verb the backup ceremony family needs once a
// single-use proof has been minted for THIS command invocation:
// VerbBackupExport/VerbBackupImport (the §D-34 opt-in vault leg,
// pre-existing) and VerbGet (S-42.T6's key ceremony, which must read back
// an already-escrowed identity/signing key rather than re-minting one
// every run -- EnsureAgeIdentity/EnsureManifestSigningKey, recovery.go).
// This does not widen what a proof can do across commands: NewVault(proof)
// builds a fresh broker scoped to one proof for one invocation, and a
// fresh proof is required every time (backup_elevation.go).
func (g backupProofGate) Authorize(_ context.Context, verb string) error {
	if g.proof == "" {
		return backup.ErrElevationRequired
	}
	switch verb {
	case secrets.VerbBackupExport, secrets.VerbBackupImport, secrets.VerbGet:
		return nil
	default:
		return backup.ErrElevationRequired
	}
}

type backupVaultAdapter struct{ broker *secrets.Broker }

func (a backupVaultAdapter) Export(ctx context.Context, req backup.VaultExportRequest) ([]byte, error) {
	return a.broker.Export(ctx, secrets.ExportRequest{Passphrase: req.Passphrase})
}

func (a backupVaultAdapter) Import(ctx context.Context, req backup.VaultImportRequest) error {
	return a.broker.Import(ctx, secrets.ImportRequest{Wrapped: req.Wrapped, Passphrase: req.Passphrase})
}

// backupVaultStoreAdapter adapts *secrets.Broker to backup.VaultStore, the
// S-42.T6 ceremony's narrow Exists/Get/Set seam.
type backupVaultStoreAdapter struct{ broker *secrets.Broker }

func (a backupVaultStoreAdapter) Exists(ctx context.Context, name string) (bool, error) {
	return a.broker.Exists(ctx, name)
}

func (a backupVaultStoreAdapter) Get(ctx context.Context, name string) ([]byte, error) {
	return a.broker.Get(ctx, name)
}

func (a backupVaultStoreAdapter) Set(ctx context.Context, name string, value []byte) error {
	_, err := a.broker.Set(ctx, name, value, secrets.SetUpdate)
	return err
}
