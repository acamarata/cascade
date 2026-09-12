// Purpose: production composition root for the v1 data importers.
// Inputs: `cascade migrate v1 <domain> --from <v1-home> [--dry-run]`.
// Outputs: the importer's deterministic result through internal/output.
// Constraints: one atomic domain runs per invocation; dependencies resolve
// only when executed; every opened durable store is closed before return.
// SPORT: migration/v1/wiring (P1-E26-W10-S53-T1).
package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/memory"
	migrationv1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

type migrateImporterFactory func(context.Context, migrationv1.Domain) (migrationv1.Importer, func() error, error)

type migrateFlags struct {
	sourceRoot string
	dryRun     bool
}

type migrateResultView struct {
	migrationv1.DryRunResult
}

func (v migrateResultView) String() string {
	mode := "applied"
	if !v.Applied {
		mode = "planned"
	}
	return fmt.Sprintf("%s: %d change(s), %d journal entry(s), %d re-auth prompt(s) [%s]",
		v.Domain, v.DeltaCount(), len(v.Journal), len(v.Reauth), mode)
}

func mountMigrateCmd(root *cobra.Command) {
	cmd := newMigrateCmd(productionMigrateFactory)
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

func newMigrateCmd(factory migrateImporterFactory) *cobra.Command {
	cmd := &cobra.Command{
		Use:         "migrate",
		Short:       "Import data from an earlier Cascade installation",
		Hidden:      true,
		Annotations: map[string]string{"local": "true"},
	}
	cmd.AddCommand(newMigrateV1Cmd(factory))
	return cmd
}

func newMigrateV1Cmd(factory migrateImporterFactory) *cobra.Command {
	var flags migrateFlags
	cmd := &cobra.Command{
		Use:   "v1 <memory|vault|accounts|config>",
		Short: "Import one v1 data domain into its v2 store",
		Long: "Import exactly one v1 data domain. Each domain validates its entire input\n" +
			"before writing and converges on an identical second run. Use --dry-run to\n" +
			"inspect the exact delta without changing a destination.",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigrateV1(cmd, factory, migrationv1.Domain(args[0]), flags)
		},
	}
	cmd.Flags().StringVar(&flags.sourceRoot, "from", "", "v1 home directory to import")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "report changes without writing")
	return cmd
}

func runMigrateV1(cmd *cobra.Command, factory migrateImporterFactory, domain migrationv1.Domain, flags migrateFlags) error {
	if flags.sourceRoot == "" {
		return cascade.New(cascade.KindInvalidInput, "migrate v1: --from is required")
	}
	if factory == nil {
		return cascade.New(cascade.KindInternal, "migrate v1: importer factory is not configured")
	}
	importer, closeStore, err := factory(cmd.Context(), domain)
	if err != nil {
		return err
	}
	result, importErr := importer.Import(cmd.Context(), migrationv1.Request{
		SourceRoot: flags.sourceRoot, DryRun: flags.dryRun,
	})
	closeErr := closeStore()
	if importErr != nil {
		return importErr
	}
	if closeErr != nil {
		return cascade.Wrap(cascade.KindUnavailable, closeErr, "migrate v1: close destination store")
	}
	return vaultOutputWriter(cmd).Result(migrateResultView{result})
}

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
