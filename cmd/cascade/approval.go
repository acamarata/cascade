// Purpose: `cascade approval` (07-CLI-COMMAND-TREE §approval) — the mount
//
//	plus list, show, deny and expire. The standing subtree and the
//	elevated grant verb live in approval_standing.go, split under
//	Art.10.3's 300-line cap.
//
// Inputs: cobra flags plus an approvalDeps injected at construction, so no
//
//	test touches a real socket or the real environment (Art.7.1).
//
// Outputs: process output through internal/output.Writer — the versioned
//
//	JSON envelope under --json, a human table otherwise.
//
// Constraints: never a hand-rolled JSON-RPC request; every verb goes
//
//	through the D/S-07.T3 client SDK. There is no embedded fallback for
//	this namespace: the approval queue lives in daemon memory by design
//	(an approval nobody is waiting on is an approval nobody gave), so a
//	daemonless invocation is REFUSED with an actionable error rather than
//	answered from a second, empty queue.
//
// SPORT: cli/approval/ADD (P1-E09-W2-S18-T6).
package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// approvalDialTimeout bounds the daemon round trip, matching the value
// every other command group in this package already uses.
const approvalDialTimeout = 5 * time.Second

// approvalDeps carries every external input the approval and policy
// command groups need, mirroring contextScopeDeps's injection pattern.
type approvalDeps struct {
	Paths       runtime.PathProvider
	Clock       runtime.Clock
	DialContext func(ctx context.Context, socketPath string) (net.Conn, error)
}

// productionApprovalDeps is the real environment.
func productionApprovalDeps() approvalDeps {
	return approvalDeps{Paths: lazyPaths{}, Clock: runtime.NewSystemClock(), DialContext: client.UnixDialer}
}

// mountApprovalCmd attaches the top-level `approval` command group. It is
// the SOLE mount point, called from root.go's mountSubcommands.
func mountApprovalCmd(root *cobra.Command) {
	cmd := newApprovalCmd(productionApprovalDeps())
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

// newApprovalCmd builds the whole `approval` tree.
func newApprovalCmd(deps approvalDeps) *cobra.Command {
	approvalCmd := &cobra.Command{
		Use:   "approval",
		Short: "Inspect and decide queued approval requests",
		Long: "Inspect and decide the actions policy evaluation queued for a human answer.\n" +
			"Granting an approval is an elevated verb: it needs a fresh local attestation\n" +
			"and the signed approval token, never the request id alone.",
	}
	approvalCmd.AddCommand(newApprovalListCmd(deps))
	approvalCmd.AddCommand(newApprovalShowCmd(deps))
	approvalCmd.AddCommand(newApprovalDenyCmd(deps))
	approvalCmd.AddCommand(newApprovalExpireCmd(deps))
	// grant and the standing subtree are built in approval_standing.go.
	approvalCmd.AddCommand(newApprovalGrantCmd(deps))
	approvalCmd.AddCommand(newApprovalStandingCmd(deps))
	return approvalCmd
}

// approvalClient resolves the daemon socket and returns a client for it. A
// daemonless invocation is refused here, once, for every verb in both
// groups.
func approvalClient(ctx context.Context, deps approvalDeps, verb string) (*client.Client, error) {
	if st, ok := runtime.DaemonlessStateFrom(ctx); ok && st.Embedded {
		return nil, cascade.Newf(cascade.KindUnavailable,
			"cascade %s needs a running daemon: the approval queue and the policy engine "+
				"live in the daemon process. Start it with `cascade daemon start`.", verb)
	}
	settings, err := daemon.ResolveSettings(nil, deps.Paths)
	if err != nil {
		return nil, err
	}
	return client.New(settings.SocketPath, client.DialFunc(deps.DialContext), approvalDialTimeout), nil
}

// approvalOutputWriter mirrors the per-file convention every other command
// group in this package follows.
func approvalOutputWriter(cmd *cobra.Command) *output.Writer {
	jsonOut, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	verbose, _ := cmd.Flags().GetBool("verbose")
	noColor, _ := cmd.Flags().GetBool("no-color")
	return output.New(cmd.OutOrStdout(), cmd.OutOrStderr(), jsonOut, quiet, verbose, noColor)
}

// newApprovalListCmd builds `approval list`.
func newApprovalListCmd(deps approvalDeps) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List approval requests awaiting a decision",
		Long:    "List every action awaiting a human decision, oldest first.",
		Example: "  cascade approval list --json",
		Args:    usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := approvalClient(cmd.Context(), deps, "approval list")
			if err != nil {
				return err
			}
			res, err := c.ApprovalList(cmd.Context())
			if err != nil {
				return err
			}
			return approvalOutputWriter(cmd).Result(approvalListView(res))
		},
	}
}

// approvalListView renders the pending queue as a table. It prints the
// three fields a PendingEntry carries and cannot print a token, because
// the value it renders holds none.
type approvalListView policy.ApprovalListResult

// String renders the queue as a human-readable table.
func (v approvalListView) String() string {
	if len(v.Pending) == 0 {
		return "no approval requests are pending"
	}
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "REQUEST\tEXPIRES\tACTION\n")
	for _, e := range v.Pending {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", e.RequestID, e.ExpiresAt.UTC().Format(time.RFC3339), e.Summary)
	}
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// newApprovalShowCmd builds `approval show <id>`.
func newApprovalShowCmd(deps approvalDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "show <request-id>",
		Short: "Show one pending approval request",
		Long: "Show one pending approval request. The response carries the request id, the\n" +
			"summary the approver is asked about and the expiry, and never the approval\n" +
			"token, its nonce or its action hash.",
		Example: "  cascade approval show 01J8ZC5W2K4F6H8M0P2R4T6V8X",
		Args:    usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := approvalClient(cmd.Context(), deps, "approval show")
			if err != nil {
				return err
			}
			entry, err := c.ApprovalShow(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return approvalOutputWriter(cmd).Result(approvalEntryView(entry))
		},
	}
}

// approvalEntryView renders one pending entry.
type approvalEntryView policy.PendingEntry

// String renders the entry as a two-column table.
func (v approvalEntryView) String() string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "FIELD\tVALUE\n")
	_, _ = fmt.Fprintf(tw, "request_id\t%s\n", v.RequestID)
	_, _ = fmt.Fprintf(tw, "action\t%s\n", v.Summary)
	_, _ = fmt.Fprintf(tw, "expires\t%s\n", v.ExpiresAt.UTC().Format(time.RFC3339))
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// newApprovalDenyCmd builds `approval deny <id>`.
func newApprovalDenyCmd(deps approvalDeps) *cobra.Command {
	var params policy.ApprovalDecisionParams
	var level uint8
	cmd := &cobra.Command{
		Use:   "deny <request-id>",
		Short: "Record a refusal for one approval request",
		Long: "Record a refusal for one approval request. The summary and rung the surface\n" +
			"displayed must be passed back, so the answer is bound to what was shown.",
		Example: `  cascade approval deny 01J8ZC5W2K4F6H8M0P2R4T6V8X --presented-summary "rm -rf /tmp/x (L4)" --presented-level 5`,
		Args:    usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := approvalClient(cmd.Context(), deps, "approval deny")
			if err != nil {
				return err
			}
			params.RequestID = args[0]
			params.PresentedLevel = policy.RiskLevel(level)
			res, err := c.ApprovalDeny(cmd.Context(), params)
			if err != nil {
				return err
			}
			return approvalOutputWriter(cmd).Result(decisionView(res))
		},
	}
	cmd.Flags().StringVar(&params.PresentedSummary, "presented-summary", "",
		"the exact summary string the surface displayed")
	cmd.Flags().Uint8Var(&level, "presented-level", 0,
		"the rung the surface displayed, as its ladder value")
	return cmd
}

// decisionView reports what a decision verb changed, rather than exiting
// silently.
type decisionView policy.DecisionResult

// String names the entry and the state the call left it in.
func (v decisionView) String() string {
	return fmt.Sprintf("request %s is now %s", v.RequestID, v.State)
}

// newApprovalExpireCmd builds `approval expire`.
func newApprovalExpireCmd(deps approvalDeps) *cobra.Command {
	return &cobra.Command{
		Use:     "expire",
		Short:   "Retire approval requests that passed their expiry",
		Long:    "Run the expiry sweep and report how many requests it retired.",
		Example: "  cascade approval expire",
		Args:    usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := approvalClient(cmd.Context(), deps, "approval expire")
			if err != nil {
				return err
			}
			res, err := c.ApprovalExpire(cmd.Context())
			if err != nil {
				return err
			}
			return approvalOutputWriter(cmd).Result(expireView(res))
		},
	}
}

// expireView reports the sweep's effect.
type expireView policy.ApprovalExpireResult

// String names how many entries were retired.
func (v expireView) String() string {
	return fmt.Sprintf("retired %d expired approval request(s)", v.Expired)
}
