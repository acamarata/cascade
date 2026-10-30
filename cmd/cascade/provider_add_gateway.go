// Purpose: compose gateway registration and credential-free re-verification.
// Inputs: CLI flags and the on-disk effective config.
// Outputs: credential-safe provider views and durable registry/lane writes.
// Constraints: never select custody for a gateway credential.
// SPORT: cli.provider.gateway/ADD.
package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/providers/gateway"
	"github.com/acamarata/cascade/internal/providers/intake"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

func runProviderAddGateway(cmd *cobra.Command, deps providerDeps, name string, flags providerAddFlags) error {
	for _, flag := range []string{"key", "key-env", "oauth", "base-url", "kind"} {
		if cmd.Flags().Changed(flag) {
			return cascade.Newf(cascade.KindInvalidInput, "provider add: --gateway cannot be combined with --%s", flag)
		}
	}
	acct, src, err := configuredGateway(cmd.Context(), deps, flags.gateway)
	if err != nil {
		return err
	}
	reg, closeReg, err := gatewayProviderRegistry(cmd.Context(), deps)
	if err != nil {
		return err
	}
	defer func() { _ = closeReg() }()
	id, err := gatewayIntakeDeps(deps, reg)
	if err != nil {
		return err
	}
	result, err := intake.AddGateway(cmd.Context(), id, intake.GatewayAddRequest{
		Name: name, Account: acct, Source: src, Pool: flags.pool, NoVerify: flags.noVerify,
	})
	if err != nil {
		return err
	}
	return vaultOutputWriter(cmd).Result(providerAddView{AddResult: result})
}

func configuredGateway(ctx context.Context, deps providerDeps, name string) (gateway.Account, gateway.Source, error) {
	if err := registry.ValidateGatewayAuthRef(registry.VaultKeyRef("gateway:" + name)); err != nil {
		return gateway.Account{}, gateway.Source{}, err
	}
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{Path: deps.Paths.ConfigPath()})
	if err != nil {
		return gateway.Account{}, gateway.Source{}, err
	}
	accounts, err := gateway.AccountsFrom(runtime.EffectiveConfigOf(cfg))
	if err != nil {
		return gateway.Account{}, gateway.Source{}, err
	}
	acct, err := gateway.Lookup(accounts, name)
	getenv := deps.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	src := gateway.Source{Getenv: getenv, Home: getenv("HOME"), User: getenv("USER")}
	return acct, src, err
}

func gatewayIntakeDeps(deps providerDeps, reg intake.Registry) (intake.Deps, error) {
	engine, err := egress.ProcessEngine(egress.DefaultRegistry())
	return intake.Deps{Doer: deps.Doer, Clock: runtime.NewSystemClock(), Registry: reg, Egress: engine}, err
}

func gatewayProviderRegistry(ctx context.Context, deps providerDeps) (intake.Registry, func() error, error) {
	if deps.Registry != nil {
		return deps.Registry, func() error { return nil }, nil
	}
	dir := deps.Paths.DataDir()
	if dir == "" {
		return nil, nil, cascade.New(cascade.KindUnavailable, "provider: data directory unavailable")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	clock := runtime.NewSystemClock()
	db, err := openMigratedDB(ctx, filepath.Join(dir, providerRegistryDBFile), func(ctx context.Context, db *sql.DB) error {
		return registry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", "")
	})
	if err != nil {
		return nil, nil, err
	}
	return newRegistryAdapter(registry.NewRegistry(db, clock)), db.Close, nil
}

func newGatewayAwareReauthCmd(deps providerDeps) *cobra.Command {
	cmd := newProviderReauthCmd(deps)
	regular := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		reg, closeReg, err := gatewayProviderRegistry(cmd.Context(), deps)
		if err != nil {
			return err
		}
		rec, lookupErr := reg.GetProvider(cmd.Context(), args[0])
		if lookupErr != nil || rec.Auth != intake.AuthGateway {
			_ = closeReg()
			return regular(cmd, args)
		}
		defer func() { _ = closeReg() }()
		return runProviderGatewayReauth(cmd, deps, reg, rec)
	}
	return cmd
}

func runProviderGatewayReauth(cmd *cobra.Command, deps providerDeps, reg intake.Registry, rec intake.ProviderRecord) error {
	for _, flag := range []string{"key", "key-env", "oauth", "no-verify"} {
		if cmd.Flags().Changed(flag) {
			return cascade.Newf(cascade.KindInvalidInput, "provider reauth: gateway records do not accept --%s", flag)
		}
	}
	if err := registry.ValidateGatewayAuthRef(registry.VaultKeyRef(rec.AuthRef)); err != nil {
		return err
	}
	acct, src, err := configuredGateway(cmd.Context(), deps, strings.TrimPrefix(string(rec.AuthRef), "gateway:"))
	if err != nil {
		return err
	}
	id, err := gatewayIntakeDeps(deps, reg)
	if err != nil {
		return err
	}
	result, err := intake.ReauthGateway(cmd.Context(), id, rec, acct, src)
	if err != nil {
		return err
	}
	return vaultOutputWriter(cmd).Result(providerAddView{AddResult: result})
}
