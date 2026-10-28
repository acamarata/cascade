// Purpose: `cascade uninstall [--dry-run] [--yes] [--purge-data]` over
//
//	internal/uninstall's typed inventory: list every Cascade-owned item,
//	confirm, then remove each through its own Remove.
//
// Inputs: the flags, the global --json flag, CASCADE_NO_INPUT, stdin for
//
//	the confirmation, and the injected uninstallDeps.
//
// Outputs: one Delta per item (removed, would-remove, absent, kept with a
//
//	note) through internal/output; exit 0 when every item converged.
//
// Constraints: a local command; no RPC method. Windows refuses
//
//	--purge-data first. CASCADE_NO_INPUT=1 without --yes (and not a dry
//	run) is a hard error before the inventory. --purge-data calls the
//	local elevation gate for uninstall.purge_data after the confirmation
//	and before any item runs; a refusal removes nothing. --yes accepts the
//	default: install items go, data stays.
//
// SPORT: cmd/cascade uninstall/ADDED.
package main

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/uninstall"
	"github.com/acamarata/cascade/pkg/cascade"
)

// purgeDataVerb is the elevated verb --purge-data authorises.
const purgeDataVerb = "uninstall.purge_data"

// uninstallDeps is every external input the command needs.
type uninstallDeps struct {
	Getenv runtime.Getenv
	GOOS   string
	// Gate authorises --purge-data. Nil refuses it.
	Gate secrets.ElevationGate
	// Inventory lists the items; production builds uninstall.Deps lazily.
	Inventory func(ctx context.Context) ([]uninstall.Item, error)
	// Confirm asks one yes/no question; nil means no one can be asked.
	Confirm func(cmd *cobra.Command, question string) (bool, error)
}

type uninstallFlags struct{ dryRun, yes, purge bool }

// newUninstallCmd builds `cascade uninstall`.
func newUninstallCmd(deps uninstallDeps) *cobra.Command {
	var f uninstallFlags
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove every Cascade-owned item from this machine",
		Long: "Remove what Cascade installed: the daemon and widget units, plugins (through\n" +
			"each plugin's teardown hook), the vault entries Cascade indexed, the executables\n" +
			"whose sha256 still matches the install receipt, and the receipt. Backups are\n" +
			"never touched. --dry-run lists every item and removes nothing.\n\n" +
			"--purge-data also removes the elevation key, the config file and the data\n" +
			"directory (backups inside it are kept). It is an elevated verb: it needs local\n" +
			"presence through the enrolled elevation helper, and Windows refuses it.\n\n" +
			"Without --yes the plan is printed and confirmed first. CASCADE_NO_INPUT=1\n" +
			"requires --yes.",
		Args:        usageArgs(cobra.NoArgs),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUninstall(cmd, deps, f)
		},
	}
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "list every item and what would happen; remove nothing")
	cmd.Flags().BoolVar(&f.yes, "yes", false, "skip the confirmation (required under CASCADE_NO_INPUT=1)")
	cmd.Flags().BoolVar(&f.purge, "purge-data", false, "also remove the elevation key, config and data directory (elevated)")
	return cmd
}

// runUninstall is the command body, in the contract's order.
func runUninstall(cmd *cobra.Command, deps uninstallDeps, f uninstallFlags) error {
	if f.purge && deps.GOOS == "windows" {
		return cascade.New(cascade.KindUnsupported,
			"uninstall: --purge-data is not supported on windows; remove the cascade home directory (CASCADE_HOME) by hand after `cascade uninstall`")
	}
	if !f.yes && !f.dryRun && (deps.Getenv == nil || deps.Getenv("CASCADE_NO_INPUT") == "1") {
		return cascade.New(cascade.KindInvalidInput,
			"uninstall: CASCADE_NO_INPUT=1 requires --yes (or --dry-run); nothing was removed")
	}
	ctx := cmd.Context()
	if deps.Inventory == nil {
		return cascade.New(cascade.KindUnavailable, "uninstall: no inventory is configured")
	}
	items, err := deps.Inventory(ctx)
	if err != nil {
		return err
	}
	plan, err := uninstall.Run(ctx, items, uninstall.Options{DryRun: true, PurgeData: f.purge})
	if f.dryRun {
		return writeUninstallReport(cmd, plan, err)
	}
	if !f.yes {
		if err := confirmUninstall(cmd, deps, plan, f.purge); err != nil {
			return err
		}
	}
	if f.purge {
		if err := authorizePurge(ctx, deps.Gate); err != nil {
			return err
		}
	}
	rep, err := uninstall.Run(ctx, items, uninstall.Options{PurgeData: f.purge})
	return writeUninstallReport(cmd, rep, err)
}

// authorizePurge runs the local elevation gate; any refusal is
// KindElevationRequired.
func authorizePurge(ctx context.Context, gate secrets.ElevationGate) error {
	if gate == nil {
		return cascade.New(cascade.KindElevationRequired,
			"uninstall: --purge-data needs the local elevation gate and none is configured; nothing was removed")
	}
	if err := gate.Authorize(ctx, purgeDataVerb); err != nil {
		if k, ok := cascade.KindOf(err); ok && k == cascade.KindElevationRequired {
			return err
		}
		return cascade.Wrap(cascade.KindElevationRequired, err, "uninstall: --purge-data was not authorised; nothing was removed")
	}
	return nil
}

// confirmUninstall prints the plan to stderr and asks once.
func confirmUninstall(cmd *cobra.Command, deps uninstallDeps, plan uninstall.Report, purge bool) error {
	if deps.Confirm == nil {
		return cascade.New(cascade.KindUnavailable, "uninstall: no confirmation reader is configured; nothing was removed")
	}
	out := cmd.ErrOrStderr()
	_, _ = fmt.Fprintln(out, "cascade uninstall will act on:")
	_, _ = fmt.Fprintln(out, uninstallView{plan}.String())
	question := "Remove these install items (data is kept)? [y/N] "
	if purge {
		question = "Remove these items INCLUDING the data directory, config and elevation key? [y/N] "
	}
	ok, err := deps.Confirm(cmd, question)
	if err != nil {
		return err
	}
	if !ok {
		return cascade.New(cascade.KindPermissionDenied, "uninstall: not confirmed; nothing was removed")
	}
	return nil
}

// confirmUninstallOnStdin reads one y/N answer; anything else is a no.
func confirmUninstallOnStdin(cmd *cobra.Command, question string) (bool, error) {
	if _, err := fmt.Fprint(cmd.ErrOrStderr(), question); err != nil {
		return false, cascade.Wrap(cascade.KindUnavailable, err, "uninstall: could not prompt for confirmation")
	}
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return false, nil
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

// writeUninstallReport renders the report, then returns runErr so a
// partial run still shows every item.
func writeUninstallReport(cmd *cobra.Command, rep uninstall.Report, runErr error) error {
	if err := outputWriter(cmd).Result(uninstallView{rep}); err != nil {
		return err
	}
	return runErr
}

// uninstallView renders a Report; JSON is {"items":[...]}.
type uninstallView struct{ uninstall.Report }

// String lists one line per item, naming every kept item's note.
func (v uninstallView) String() string {
	if len(v.Items) == 0 {
		return "nothing to remove"
	}
	var b strings.Builder
	for i, d := range v.Items {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%-12s %-15s %s", d.Action, d.Kind, d.Locator)
		if d.Note != "" {
			fmt.Fprintf(&b, " (%s)", d.Note)
		}
	}
	return b.String()
}
