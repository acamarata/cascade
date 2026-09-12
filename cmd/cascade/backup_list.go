// Purpose: implement the read-only backup snapshot listing shared by CLI and MCP.
// Inputs: the persisted target registry, target drivers, manifests, and outcomes.
// Outputs: deterministic snapshot metadata with no secret values.
// Constraints: listing never asks for elevation and an empty registry is success.
// SPORT: cmd.cascade.backup-list/ADD (P1-E19-W4-S42-T3).
package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

type backupListResult struct {
	Snapshots []backup.SnapshotSummary `json:"snapshots"`
}

func newBackupListCmd(deps backupDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List backup snapshots",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			rows, err := backupSnapshotLister(deps)(cmd.Context())
			if err != nil {
				return err
			}
			return backupOutputWriter(cmd).Result(backupListResult{Snapshots: rows})
		},
	}
}

func backupSnapshotLister(deps backupDeps) backup.SnapshotListFunc {
	return func(ctx context.Context) ([]backup.SnapshotSummary, error) {
		rt, err := deps.Open(ctx)
		if err != nil {
			return nil, err
		}
		defer rt.Close()
		targets, err := namedBackupTargets(ctx, deps, rt.Store)
		if err != nil {
			return nil, err
		}
		return backup.ListSnapshots(ctx, rt.Store, backupRegistryNamespace, targets)
	}
}

func namedBackupTargets(ctx context.Context, deps backupDeps, store provider.Store) ([]backup.NamedTarget, error) {
	records, err := backup.ListTargets(ctx, store, backupRegistryNamespace)
	if err != nil {
		return nil, err
	}
	out := make([]backup.NamedTarget, 0, len(records))
	for _, record := range records {
		target, berr := deps.BuildTarget(ctx, record, nil, deps.Getenv)
		if berr != nil {
			return nil, berr
		}
		out = append(out, backup.NamedTarget{Name: record.Name, Target: target})
	}
	return out, nil
}

func selectBackupRecord(ctx context.Context, store provider.Store, name string) (backup.TargetRecord, error) {
	if name != "" {
		return backup.GetTarget(ctx, store, backupRegistryNamespace, name)
	}
	records, err := backup.ListTargets(ctx, store, backupRegistryNamespace)
	if err != nil {
		return backup.TargetRecord{}, err
	}
	if len(records) == 0 {
		return backup.TargetRecord{}, cascade.New(cascade.KindNotFound, "backup: no target is configured")
	}
	if len(records) != 1 {
		return backup.TargetRecord{}, cascade.New(cascade.KindInvalidInput,
			"backup: more than one target is configured; select one with --target")
	}
	return records[0], nil
}

func latestSnapshot(rows []backup.SnapshotSummary, target string) (backup.SnapshotSummary, error) {
	for _, row := range rows {
		if row.Target == target {
			return row, nil
		}
	}
	return backup.SnapshotSummary{}, cascade.Newf(cascade.KindNotFound,
		"backup: target %q has no snapshots", target)
}
