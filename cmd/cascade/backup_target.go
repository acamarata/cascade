// Purpose: implement backup target add, list, and remove over the T1 registry.
// Inputs: NAME, one of fs/s3/rclone, non-secret driver config, and policy flags.
// Outputs: persisted target and cadence metadata only.
// Constraints: credentials are never accepted; target validation runs before write.
// SPORT: cmd.cascade.backup-target/ADD (P1-E19-W4-S42-T3).
package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

type backupTargetView struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	LocationRef string   `json:"location_ref"`
	Cron        string   `json:"cron"`
	Domains     []string `json:"domains"`
}

func newBackupTargetCmd(deps backupDeps) *cobra.Command {
	cmd := &cobra.Command{Use: "target", Short: "Manage fs, s3, and rclone backup targets"}
	cmd.AddCommand(newBackupTargetAddCmd(deps), newBackupTargetListCmd(deps), newBackupTargetRemoveCmd(deps))
	return cmd
}

func newBackupTargetAddCmd(deps backupDeps) *cobra.Command {
	var cron string
	var domains []string
	cmd := &cobra.Command{
		Use:   "add NAME (fs|s3|rclone) LOCATION_REF",
		Short: "Add a backup target and its schedule policy",
		Args:  usageArgs(cobra.ExactArgs(3)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateBackupDomains(domains); err != nil {
				return err
			}
			record, err := backupTargetRecord(args[0], args[1], args[2])
			if err != nil {
				return err
			}
			return addBackupTarget(cmd.Context(), cmd, deps, record,
				backup.TargetPolicy{Target: record.Name, CronSpec: cron, Domains: domains})
		},
	}
	cmd.Flags().StringVar(&cron, "cron", "", "cron schedule for this target")
	cmd.Flags().StringSliceVar(&domains, "domain", nil, "domain to capture (repeatable)")
	_ = cmd.MarkFlagRequired("cron")
	_ = cmd.MarkFlagRequired("domain")
	return cmd
}

func backupTargetRecord(name, kind, location string) (backup.TargetRecord, error) {
	record := backup.TargetRecord{Name: name, Kind: backup.TargetKind(kind)}
	switch record.Kind {
	case backup.TargetKindFS:
		record.FSRoot = location
	case backup.TargetKindS3:
		record.S3EnvPrefix = location
	case backup.TargetKindRclone:
		record.RcloneRemote = location
	default:
		return backup.TargetRecord{}, cascade.Newf(cascade.KindInvalidInput,
			"backup: unknown target kind %q; want fs, s3, or rclone", kind)
	}
	if err := record.Validate(); err != nil {
		return backup.TargetRecord{}, err
	}
	return record, nil
}

func validateBackupDomains(domains []string) error {
	if len(domains) == 0 {
		return cascade.New(cascade.KindInvalidInput, "backup: at least one --domain is required")
	}
	known := make(map[string]bool, len(storage.AllDomains))
	for _, domain := range storage.AllDomains {
		known[string(domain.ID)] = true
	}
	for _, domain := range domains {
		if !known[domain] {
			return cascade.Newf(cascade.KindInvalidInput, "backup: unknown domain %q", domain)
		}
	}
	return nil
}

func addBackupTarget(ctx context.Context, cmd *cobra.Command, deps backupDeps, record backup.TargetRecord, policy backup.TargetPolicy) error {
	rt, err := deps.Open(ctx)
	if err != nil {
		return err
	}
	defer rt.Close()
	if err := backup.PutTarget(ctx, rt.Store, backupRegistryNamespace, record); err != nil {
		return err
	}
	if err := backup.PutPolicy(ctx, rt.Store, backupRegistryNamespace, policy); err != nil {
		_ = backup.DeleteTarget(ctx, rt.Store, backupRegistryNamespace, record.Name)
		return err
	}
	return backupOutputWriter(cmd).Result(targetView(record, policy))
}

func newBackupTargetListCmd(deps backupDeps) *cobra.Command {
	return &cobra.Command{
		Use: "list", Short: "List configured backup targets", Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			rt, err := deps.Open(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.Close()
			views, err := backupTargetViews(cmd.Context(), rt)
			if err != nil {
				return err
			}
			return backupOutputWriter(cmd).Result(map[string]any{"targets": views})
		},
	}
}

func backupTargetViews(ctx context.Context, rt *backupRuntime) ([]backupTargetView, error) {
	records, err := backup.ListTargets(ctx, rt.Store, backupRegistryNamespace)
	if err != nil {
		return nil, err
	}
	views := make([]backupTargetView, 0, len(records))
	for _, record := range records {
		policy, perr := backup.GetPolicy(ctx, rt.Store, backupRegistryNamespace, record.Name)
		if perr != nil {
			return nil, perr
		}
		views = append(views, targetView(record, policy))
	}
	return views, nil
}

func targetView(record backup.TargetRecord, policy backup.TargetPolicy) backupTargetView {
	location := record.FSRoot
	switch record.Kind {
	case backup.TargetKindFS:
		// location already defaults to record.FSRoot above.
	case backup.TargetKindS3:
		location = record.S3EnvPrefix
	case backup.TargetKindRclone:
		location = record.RcloneRemote
	}
	return backupTargetView{
		Name: record.Name, Kind: string(record.Kind), LocationRef: location,
		Cron: policy.CronSpec, Domains: policy.Domains,
	}
}

func newBackupTargetRemoveCmd(deps backupDeps) *cobra.Command {
	return &cobra.Command{
		Use: "remove NAME", Short: "Remove a configured backup target", Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := deps.Open(cmd.Context())
			if err != nil {
				return err
			}
			defer rt.Close()
			if _, err := backup.GetTarget(cmd.Context(), rt.Store, backupRegistryNamespace, args[0]); err != nil {
				return err
			}
			if err := backup.DeleteTarget(cmd.Context(), rt.Store, backupRegistryNamespace, args[0]); err != nil {
				return err
			}
			return backupOutputWriter(cmd).Result(map[string]any{"name": args[0], "removed": true})
		},
	}
}
