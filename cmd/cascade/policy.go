// Purpose: `cascade policy` (07-CLI-COMMAND-TREE §policy) — explain,
//
//	check, list and audit query. All four are read-only.
//
// Inputs: cobra flags and positional arguments, plus the approvalDeps
//
//	approval.go injects, so no test touches a real socket.
//
// Outputs: process output through internal/output.Writer — the versioned
//
//	JSON envelope under --json, a human rendering otherwise.
//
// Constraints: every verb goes through the D/S-07.T3 client SDK, never a
//
//	hand-rolled request. These verbs evaluate; they never grant. A
//	daemonless invocation is refused by approvalClient with an actionable
//	error rather than answered by a second engine that would not be the
//	one the daemon decides with.
//
// SPORT: cli/policy/ADD (P1-E09-W2-S18-T6).
package main

import (
	"bytes"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/policy"
)

// mountPolicyCmd attaches the top-level `policy` command group. It is the
// SOLE mount point, called from root.go's mountSubcommands.
func mountPolicyCmd(root *cobra.Command) {
	cmd := newPolicyCmd(productionApprovalDeps())
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

// newPolicyCmd builds the whole `policy` tree.
func newPolicyCmd(deps approvalDeps) *cobra.Command {
	policyCmd := &cobra.Command{
		Use:   "policy",
		Short: "Explain and inspect policy decisions",
		Long: "Ask the running policy engine what it would decide, and read the\n" +
			"append-only record of what it did decide.",
	}
	policyCmd.AddCommand(newPolicyExplainCmd(deps))
	policyCmd.AddCommand(newPolicyCheckCmd(deps))
	policyCmd.AddCommand(newPolicyListCmd(deps))
	policyCmd.AddCommand(newPolicyAuditCmd(deps))
	// risk is the AH/S-69.T1 sole exception to this file's own "every
	// verb goes through the daemon" posture (see policy_risk.go's own
	// doc comment): risk classification is a pure local computation,
	// not an Engine evaluation.
	policyCmd.AddCommand(newPolicyRiskCmd())
	return policyCmd
}

// evalFlags attaches the two flags both evaluating verbs take.
func evalFlags(cmd *cobra.Command, params *policy.EvalParams, kind, id *string) {
	cmd.Flags().StringVar(&params.Capability, "capability", "",
		"the registered capability the action would need")
	cmd.Flags().StringVar(kind, "subject-kind", "", "the subject's kind, for example user or agent")
	cmd.Flags().StringVar(id, "subject-id", "", "the subject's identifier")
}

// newPolicyExplainCmd builds `policy explain <action>`.
func newPolicyExplainCmd(deps approvalDeps) *cobra.Command {
	var params policy.EvalParams
	var kind, id string
	cmd := &cobra.Command{
		Use:   "explain <action>",
		Short: "Explain how the policy engine would decide one action",
		Long: "Evaluate one action and print the whole decision path: every layer that\n" +
			"ran, the rung the action classified at and the layer that decided.",
		Example: `  cascade policy explain "rm -rf /tmp/x"`,
		Args:    usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := approvalClient(cmd.Context(), deps, "policy explain")
			if err != nil {
				return err
			}
			params.Action = args[0]
			params.Subject = policy.Subject{Kind: policy.SubjectKind(kind), ID: id}
			res, err := c.PolicyExplain(cmd.Context(), params)
			if err != nil {
				return err
			}
			return approvalOutputWriter(cmd).Result(policyExplainView(res))
		},
	}
	evalFlags(cmd, &params, &kind, &id)
	return cmd
}

// policyExplainView renders one evaluation and its trace.
type policyExplainView policy.ExplainResult

// String renders the verdict, the rung and the layer-by-layer path.
func (v policyExplainView) String() string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "FIELD\tVALUE\n")
	_, _ = fmt.Fprintf(tw, "verdict\t%s\n", v.Verdict)
	_, _ = fmt.Fprintf(tw, "level\t%s\n", v.Level)
	_, _ = fmt.Fprintf(tw, "layer\t%s\n", v.Layer)
	_, _ = fmt.Fprintf(tw, "matched_rule\t%s\n", v.MatchedRule)
	_, _ = fmt.Fprintf(tw, "reason\t%s\n", v.Reason)
	_ = tw.Flush()
	out := strings.TrimRight(buf.String(), "\n")
	if v.Explanation != "" {
		out += "\n\n" + v.Explanation
	}
	return out
}

// newPolicyCheckCmd builds `policy check <command>`.
func newPolicyCheckCmd(deps approvalDeps) *cobra.Command {
	var params policy.EvalParams
	var kind, id string
	cmd := &cobra.Command{
		Use:     "check <command>",
		Short:   "Report the verdict for one command, without the trace",
		Long:    "Evaluate one command and print the verdict and the rung alone.",
		Example: `  cascade policy check "git push" --json`,
		Args:    usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := approvalClient(cmd.Context(), deps, "policy check")
			if err != nil {
				return err
			}
			params.Action = args[0]
			params.Subject = policy.Subject{Kind: policy.SubjectKind(kind), ID: id}
			res, err := c.PolicyCheck(cmd.Context(), params)
			if err != nil {
				return err
			}
			return approvalOutputWriter(cmd).Result(policyCheckView(res))
		},
	}
	evalFlags(cmd, &params, &kind, &id)
	return cmd
}

// policyCheckView renders the short answer.
type policyCheckView policy.CheckResult

// String names the verdict and the rung.
func (v policyCheckView) String() string {
	return fmt.Sprintf("%s at %s (auto-advance %t)", v.Verdict, v.Level, v.AutoAdvance)
}

// newPolicyListCmd builds `policy list`.
func newPolicyListCmd(deps approvalDeps) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List the registered capabilities and policy verbs",
		Long:    "List the capabilities the running daemon registered and the verbs it dispatches.",
		Example: "  cascade policy list --json",
		Args:    usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := approvalClient(cmd.Context(), deps, "policy list")
			if err != nil {
				return err
			}
			res, err := c.PolicyList(cmd.Context())
			if err != nil {
				return err
			}
			return approvalOutputWriter(cmd).Result(policyListView(res))
		},
	}
}

// policyListView renders the capabilities and the verb table.
type policyListView policy.ListResult

// String renders both tables, capabilities first.
func (v policyListView) String() string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "CAPABILITY\tCLASS\n")
	for _, c := range v.Capabilities {
		_, _ = fmt.Fprintf(tw, "%s\t%s\n", c.Name, c.DefaultPolicy)
	}
	_, _ = fmt.Fprintf(tw, "\nVERB\tRISK\tELEVATED\n")
	for _, verb := range v.Verbs {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%t\n", verb.Method, verb.Risk, verb.Elevated)
	}
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// newPolicyAuditCmd builds the `policy audit` subtree, whose one verb is
// query.
func newPolicyAuditCmd(deps approvalDeps) *cobra.Command {
	audit := &cobra.Command{
		Use:   "audit",
		Short: "Read the append-only decision record",
	}
	audit.AddCommand(newPolicyAuditQueryCmd(deps))
	return audit
}

// newPolicyAuditQueryCmd builds `policy audit query`.
func newPolicyAuditQueryCmd(deps approvalDeps) *cobra.Command {
	var filter []string
	cmd := &cobra.Command{
		Use:   "query",
		Short: "Query the append-only decision record",
		Long: "Read one page of the append-only decision record, oldest first. Every\n" +
			"record the walk passes over is verified, so an altered record is reported\n" +
			"rather than absorbed into the answer.",
		Example: "  cascade policy audit query --filter kind=policy.decision --filter limit=20",
		Args:    usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := approvalClient(cmd.Context(), deps, "policy audit query")
			if err != nil {
				return err
			}
			res, err := c.PolicyAuditQuery(cmd.Context(), policy.AuditQueryParams{Filter: filter})
			if err != nil {
				return err
			}
			return approvalOutputWriter(cmd).Result(policyAuditView(res))
		},
	}
	cmd.Flags().StringArrayVar(&filter, "filter", nil,
		"a key=value filter token, repeatable (for example kind=policy.decision)")
	return cmd
}

// policyAuditView renders one page of audit records.
type policyAuditView policy.AuditQueryResult

// String renders the page as a table, with the cursor when one remains.
func (v policyAuditView) String() string {
	if len(v.Records) == 0 {
		return "no audit records matched"
	}
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "SEQ\tTIME\tKIND\tVERDICT\n")
	for _, r := range v.Records {
		_, _ = fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n",
			r.Seq, r.Time().UTC().Format(time.RFC3339), r.Kind, r.Verdict)
	}
	_ = tw.Flush()
	out := strings.TrimRight(buf.String(), "\n")
	if v.NextCursor != "" {
		out += "\n\nmore records remain; resume with cursor=" + v.NextCursor
	}
	return out
}
