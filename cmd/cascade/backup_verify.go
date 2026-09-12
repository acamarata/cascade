// Purpose: implement the read-only `backup verify [--target] [--json]`
// (07 §backup: verify ✦) -- the one-shot, non-interactive equivalent of
// S-42.T4's scheduled verification cron (06 §5.8 automation parity):
// identical RunVerification call, identical VerificationReport schema.
// Inputs: the persisted target registry and S-41.T4's real integrity gate,
// via deps.Verify.
// Outputs: a VerificationReport (pass) or a typed refusal (fail);
// --json emits the report/refusal as a structured envelope in both cases.
// Constraints: read-only, never elevated -- no proof is minted here.
// SPORT: cmd.cascade.backup-verify/ADD (P1-E19-W4-S42-T4).
package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/pkg/plugin"
)

// backupVerifyLong documents the one platform difference this ticket's
// contract asks for (Art.5): on a daemon-capable platform, this exact
// check also runs unattended on the target's own S-42.T4 verification
// cron; on Windows (tier-2 -- daemon_windows.go: "there is no daemon at
// all") no cron ever runs, so this command is the ONLY verification path
// there. See this ticket's journal for why this lives in --help text
// rather than a daemon-startup log line: no Windows daemon startup event
// exists to emit one from.
const backupVerifyLong = "Verify a target's most recent snapshot integrity.\n\n" +
	"On a platform with a daemon, the identical check also runs unattended on " +
	"that target's own verification cron (24h default, per-target policy data). " +
	"On Windows (tier-2: no daemon service exists) this command is the only " +
	"verification path -- schedule it yourself, e.g. via Windows Task Scheduler."

func newBackupVerifyCmd(deps backupDeps) *cobra.Command {
	var targetName string
	cmd := &cobra.Command{
		Use: "verify", Short: "Verify a target's most recent snapshot integrity",
		Long: backupVerifyLong, Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBackupVerify(cmd, deps, targetName)
		},
	}
	cmd.Flags().StringVar(&targetName, "target", "", "configured target name")
	return cmd
}

func runBackupVerify(cmd *cobra.Command, deps backupDeps, targetName string) error {
	rt, err := deps.Open(cmd.Context())
	if err != nil {
		return err
	}
	defer rt.Close()
	record, err := selectBackupRecord(cmd.Context(), rt.Store, targetName)
	if err != nil {
		return err
	}
	sink := deps.NewAttentionSink(rt.Store)
	report, err := deps.Verify(cmd.Context(), rt.Store, backupRegistryNamespace, record, nil, deps.Getenv, deps.Clock, sink)
	if err != nil {
		return err
	}
	return backupOutputWriter(cmd).Result(report)
}

// backupVerifyRunner adapts deps into the backup.VerifyRunFunc the
// cascade_backup_verify MCP registration (mcp.go/daemon_unix_run.go's
// composition roots) calls -- the exact same selectBackupRecord +
// deps.Verify path runBackupVerify above drives, so the MCP tool and the
// CLI verb are provably the same operation, not two independent copies.
func backupVerifyRunner(deps backupDeps) backup.VerifyRunFunc {
	return func(ctx context.Context, target string) (backup.VerificationReport, error) {
		rt, err := deps.Open(ctx)
		if err != nil {
			return backup.VerificationReport{}, err
		}
		defer rt.Close()
		record, err := selectBackupRecord(ctx, rt.Store, target)
		if err != nil {
			return backup.VerificationReport{}, err
		}
		sink := deps.NewAttentionSink(rt.Store)
		return deps.Verify(ctx, rt.Store, backupRegistryNamespace, record, nil, deps.Getenv, deps.Clock, sink)
	}
}

// daemonMCPToolRegistry builds the daemon's real MCP tool registry --
// cascade_backup_list plus S-42.T4's cascade_backup_verify, both over one
// shared productionBackupDeps() so both surfaces resolve the same
// registry/store. Split out of daemon_unix_run.go's buildRPCServer purely
// to stay under Art.10.3's 50-line function cap.
func daemonMCPToolRegistry() *mcp.ToolRegistry {
	bDeps := productionBackupDeps()
	return mcp.NewToolRegistry(plugin.Builtins,
		backup.MCPRegistration(backupSnapshotLister(bDeps)),
		backup.VerifyMCPRegistration(backupVerifyRunner(bDeps)))
}
