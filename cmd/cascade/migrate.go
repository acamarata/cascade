// Purpose: `cascade migrate v1 --from <v1-home> [--dry-run] [--yes]`
// (07-CLI-COMMAND-TREE §migrate) — the top-level orchestrator that runs
// every S-53.T1 v1 importer (config -> vault -> accounts -> memory) in
// sequence via internal/migration.MigrateV1, tracking per-domain
// completion in the migration ledger so a re-run against an
// already-migrated v1 directory converges to a no-op.
//
// Inputs: cobra flags; a migrateDeps injected at construction so a test
// never opens a real cascade.db or touches a real v1 directory.
// Outputs: the report through internal/output; a typed taxonomy error.
// Constraints: --from is required; --dry-run makes no writes; without
// --yes, CASCADE_NO_INPUT=1 refuses rather than prompting.
//
// CONTRACT NOTE (daemon dispatch, recorded rather than silently
// skipped). This ticket's full_desc calls for probing the daemon socket
// and, when a daemon is live, dispatching the migration as a JSON-RPC
// job with SSE progress — "no behavioural difference, only transport."
// This file probes (runtime.DaemonlessStateFrom, already populated by
// root.go's PersistentPreRunE for every command) and reports when a
// daemon is live, but always executes MigrateV1 in-process: a
// `migrate.v1` daemon-side RPC handler does not exist anywhere in
// internal/daemon or internal/rpc, and this ticket's files_scope does
// not include either package. Since the contract itself promises no
// behavioral difference between the two transports, always running
// in-process is a safe, spec-compliant degradation — see the journal for
// this ticket's full accounting.
//
// SPORT: cmd.cascade.migrate/CHANGED (P1-E26-W10-S53-T4).
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/migration"
	migrationv1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

// migrateFlags holds `migrate v1`'s flag values.
type migrateFlags struct {
	sourceRoot string
	dryRun     bool
	yes        bool
}

// migrateDeps carries migrate's external inputs, following provider.go's
// providerDeps pattern: a test substitutes every field so no test opens a
// real cascade.db, reads the real environment, or drives a real terminal.
type migrateDeps struct {
	Factory    migration.ImporterFactory
	OpenLedger func(ctx context.Context) (*migration.LedgerStore, func() error, error)
	Getenv     func(string) string
}

// productionMigrateDeps builds migrateDeps against the real environment.
func productionMigrateDeps() migrateDeps {
	return migrateDeps{Factory: productionMigrateFactory, OpenLedger: productionOpenLedger, Getenv: os.Getenv}
}

// mountMigrateCmd attaches the top-level `migrate` command, following
// mountVaultCmd's pattern.
func mountMigrateCmd(root *cobra.Command) {
	cmd := newMigrateCmd(productionMigrateDeps())
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

// newMigrateCmd builds the `migrate` parent noun. It stays Hidden per
// 07-CLI-COMMAND-TREE's `[local]` marking (not shown in top-level help,
// matching self-update/uninstall/docs-gen's own Hidden treatment) —
// golden_help.txt needs no change for this ticket.
func newMigrateCmd(deps migrateDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:         "migrate",
		Short:       "Import data from an earlier Cascade installation",
		Hidden:      true,
		Annotations: map[string]string{"local": "true"},
	}
	cmd.AddCommand(newMigrateV1Cmd(deps))
	return cmd
}

// newMigrateV1Cmd builds `cascade migrate v1`.
func newMigrateV1Cmd(deps migrateDeps) *cobra.Command {
	var flags migrateFlags
	cmd := &cobra.Command{
		Use:   "v1",
		Short: "Import every v1 data domain into their v2 stores",
		Long: "Import config, vault.env, accounts and memory from a v1 cascade\n" +
			"installation, in that order. A domain the ledger already records as\n" +
			"migrated is skipped, so a re-run against the same --from converges\n" +
			"to a no-op. Use --dry-run to preview the exact per-domain delta\n" +
			"without changing any destination or the ledger.",
		Example: "  cascade migrate v1 --from ../cascade-v1 --dry-run\n" +
			"  cascade migrate v1 --from ../cascade-v1 --yes",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runMigrateV1(cmd, deps, flags)
		},
	}
	cmd.Flags().StringVar(&flags.sourceRoot, "from", "", "v1 home directory to import (required)")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "preview the migration; make no writes")
	cmd.Flags().BoolVar(&flags.yes, "yes", false, "confirm the migration without prompting")
	return cmd
}

// runMigrateV1 is `migrate v1`'s composition: open the ledger, note (but
// never act on) daemon liveness, run the orchestrator, and render the
// report before returning any error — mirroring
// context_sync_projects.go's runContextSyncProjects: the report is data
// and is always written; a failing run's error is returned afterward so
// the process still exits non-zero.
func runMigrateV1(cmd *cobra.Command, deps migrateDeps, flags migrateFlags) error {
	if deps.Factory == nil || deps.OpenLedger == nil {
		return cascade.New(cascade.KindInternal, "cascade migrate v1: command is not configured")
	}
	noteDaemonLiveness(cmd)

	ledger, closeLedger, err := deps.OpenLedger(cmd.Context())
	if err != nil {
		return err
	}
	defer func() { _ = closeLedger() }()

	getenv := deps.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	opts := migration.MigrateV1Options{
		SourceRoot: flags.sourceRoot,
		DryRun:     flags.dryRun,
		Yes:        flags.yes,
		NoInput:    getenv("CASCADE_NO_INPUT") == "1",
		Confirm:    migrateConfirmFunc(cmd),
	}
	report, runErr := migration.MigrateV1(cmd.Context(), ledger, deps.Factory, opts)
	if writeErr := migrateOutputWriter(cmd).Result(migrateReportView{report}); writeErr != nil {
		return writeErr
	}
	return runErr
}

// noteDaemonLiveness prints an informational note when a live daemon is
// detected. See this file's CONTRACT NOTE doc comment: this command
// always runs embedded regardless of the probe result.
func noteDaemonLiveness(cmd *cobra.Command) {
	st, ok := runtime.DaemonlessStateFrom(cmd.Context())
	if !ok || st.Embedded {
		return
	}
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(),
		"note: a cascade daemon is running; migrate v1 still runs embedded (no daemon transport is available yet)")
}

// migrateConfirmFunc builds the interactive y/N prompt runMigrateV1 offers
// when neither --yes nor CASCADE_NO_INPUT settles the question, mirroring
// confirmBackupOperation's exact stdin/stderr seam (backup_elevation.go).
func migrateConfirmFunc(cmd *cobra.Command) func() (bool, error) {
	return func() (bool, error) {
		prompt := "cascade migrate v1 will import config, vault.env, accounts and memory. Continue? [y/N] "
		if _, err := cmd.ErrOrStderr().Write([]byte(prompt)); err != nil {
			return false, cascade.Wrap(cascade.KindUnavailable, err, "migrate v1: write confirmation prompt")
		}
		line := make([]byte, 16)
		n, _ := cmd.InOrStdin().Read(line)
		answer := strings.ToLower(strings.TrimSpace(string(line[:n])))
		return answer == "y" || answer == "yes", nil
	}
}

// migrateOutputWriter mirrors vaultOutputWriter's established per-file
// convention.
func migrateOutputWriter(cmd *cobra.Command) *output.Writer {
	jsonOut, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	verbose, _ := cmd.Flags().GetBool("verbose")
	noColor, _ := cmd.Flags().GetBool("no-color")
	return output.New(cmd.OutOrStdout(), cmd.OutOrStderr(), jsonOut, quiet, verbose, noColor)
}

// migrateReportView renders migration.MigrateV1Report for both --json
// (default json.Marshal of the embedded, json-tagged struct) and human
// output (String below).
type migrateReportView struct{ migration.MigrateV1Report }

// String renders one line per domain, then a refusal notice when the run
// was refused.
func (v migrateReportView) String() string {
	var buf bytes.Buffer
	for _, d := range v.Domains {
		if d.Skipped {
			fmt.Fprintf(&buf, "%s: already migrated -- skipped\n", d.Domain)
			continue
		}
		mode := "planned"
		if d.Result.Applied {
			mode = "applied"
		}
		status := "ok"
		if d.Error != "" {
			status = "error: " + d.Error
		}
		fmt.Fprintf(&buf, "%s: %d change(s) [%s] %s\n", d.Domain, d.Result.DeltaCount(), mode, status)
	}
	if v.Refused {
		buf.WriteString("migration was not confirmed; pass --yes to proceed\n")
	}
	return strings.TrimRight(buf.String(), "\n")
}

// productionMigrateFactory builds the real, per-domain Importer and its
// store closer. It is passed to internal/migration.MigrateV1 unmodified
// as an ImporterFactory (Art.1: not duplicated, only reused) and is also
// the sole factory `migrate_factory_test.go`'s tests exercise directly.
func productionMigrateFactory(ctx context.Context, domain migrationv1.Domain) (migrationv1.Importer, func() error, error) {
	paths := lazyPaths{}
	noClose := func() error { return nil }
	switch domain {
	case migrationv1.DomainMemory:
		root := paths.Root()
		if root == "" {
			return nil, nil, cascade.New(cascade.KindUnavailable, "migrate v1 memory: destination root unavailable")
		}
		store := memory.NewFileStore(filepath.Join(root, "memory"), runtime.NewSystemClock())
		return migrationv1.NewMemoryImporter(store), noClose, nil
	case migrationv1.DomainVault:
		broker, err := vaultBroker(productionVaultDeps())
		if err != nil {
			return nil, nil, err
		}
		return migrationv1.NewVaultImporter(broker), noClose, nil
	case migrationv1.DomainAccounts:
		store, err := openProviderStorage(ctx, productionProviderDeps())
		if err != nil {
			return nil, nil, err
		}
		return migrationv1.NewAccountsImporter(store.Registry), store.Close, nil
	case migrationv1.DomainConfig:
		return migrationv1.NewConfigImporter(paths.ConfigPath()), noClose, nil
	default:
		return nil, nil, cascade.Newf(cascade.KindInvalidInput,
			"migrate v1: unknown domain %q", domain)
	}
}

// productionOpenLedger opens the real, on-disk cascade.db (providers/sqlite,
// cross-platform, no daemon dependency — the exact pattern
// cmd/cascade/doctor_recall_index.go's buildRecallIndexManager already
// uses for a CLI-side connection to a domain that isn't the daemon's own)
// and wraps it in a migration.LedgerStore.
func productionOpenLedger(ctx context.Context) (*migration.LedgerStore, func() error, error) {
	dataDir := lazyPaths{}.DataDir()
	if dataDir == "" {
		return nil, nil, cascade.New(cascade.KindUnavailable, "cascade migrate v1: could not resolve the cascade data directory")
	}
	return productionOpenLedgerAt(ctx, dataDir)
}

// productionOpenLedgerAt is productionOpenLedger with the data directory
// injected, split out so migrate_test.go can point it at a t.TempDir()
// instead of the operator's real cascade home.
func productionOpenLedgerAt(ctx context.Context, dataDir string) (*migration.LedgerStore, func() error, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "cascade migrate v1: create data directory")
	}
	dbPath := filepath.Join(dataDir, "cascade.db")
	driver, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "cascade migrate v1: open cascade.db")
	}
	ledger, err := migration.NewLedgerStore(driver, runtime.NewSystemClock())
	if err != nil {
		_ = driver.Close()
		return nil, nil, err
	}
	return ledger, driver.Close, nil
}
