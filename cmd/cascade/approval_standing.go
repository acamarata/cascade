// Purpose: `cascade approval grant` and the `cascade approval standing`
//
//	subtree (07-CLI-COMMAND-TREE §approval). Split from approval.go under
//	Art.10.3's 300-line cap; approval.go holds the mount and the
//	non-elevated verbs.
//
// Inputs: cobra flags plus the approvalDeps approval.go injects.
// Outputs: process output through internal/output.Writer.
// Constraints: grant, standing create and standing change are ELEVATED
//
//	verbs. This file adds no elevation decision of its own: it reports the
//	daemon's ElevationRequired refusal with an actionable next step, and on
//	Windows it refuses locally before dialling, because no elevation flow
//	exists there at all (tier-2). Nor does it add a second deny-list
//	guard: a denied action class is refused by CreateStandingGrant, on the
//	far side, before any row is written.
//
// SPORT: cli/approval-standing/ADD (P1-E09-W2-S18-T6).
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// newApprovalGrantCmd builds `approval grant <id> --token <signed-token>`.
func newApprovalGrantCmd(deps approvalDeps) *cobra.Command {
	var params policy.ApprovalGrantParams
	cmd := &cobra.Command{
		Use:   "grant <request-id>",
		Short: "Redeem a signed approval token for one request",
		Long: "Redeem a signed approval token. The token is verified before anything is\n" +
			"written, so a forged or expired token changes no queue state. Knowing a\n" +
			"request id is never enough on its own, and this verb needs a fresh local\n" +
			"attestation as well.",
		Example: "  cascade approval grant 01J8ZC5W2K4F6H8M0P2R4T6V8X --token \"$TOKEN\"",
		Args:    usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseElevatedOnUnsupportedPlatform("approval grant"); err != nil {
				return err
			}
			if params.SignedToken == "" {
				return cascade.New(cascade.KindInvalidInput,
					"cascade approval grant needs the signed approval token (--token)")
			}
			c, err := approvalClient(cmd.Context(), deps, "approval grant")
			if err != nil {
				return err
			}
			params.RequestID = args[0]
			res, err := c.ApprovalGrant(cmd.Context(), params)
			if err != nil {
				return err
			}
			return approvalOutputWriter(cmd).Result(grantView(res))
		},
	}
	cmd.Flags().StringVar(&params.SignedToken, "token", "",
		"the base64 signed approval token issued for this request")
	cmd.Flags().StringVar(&params.Action, "action", "",
		"the action about to run, re-hashed against the approved digests")
	return cmd
}

// grantView reports what a redemption changed.
type grantView policy.ApprovalGrantResult

// String names the redeemed request and the terms it was approved under.
func (v grantView) String() string {
	return fmt.Sprintf("request %s redeemed at %s (capability %s, %s)",
		v.RequestID, v.ConsumedAt, v.Capability, v.Level)
}

// newApprovalStandingCmd builds the `approval standing` subtree.
func newApprovalStandingCmd(deps approvalDeps) *cobra.Command {
	standing := &cobra.Command{
		Use:   "standing",
		Short: "List and manage standing grants",
		Long: "Manage approvals given once and honoured repeatedly. A deny-listed action\n" +
			"class and an elevation-class verb can never be granted standing.",
	}
	standing.AddCommand(newStandingListCmd(deps))
	standing.AddCommand(newStandingWriteCmd(deps, "create"))
	standing.AddCommand(newStandingWriteCmd(deps, "change"))
	standing.AddCommand(newStandingRevokeCmd(deps))
	return standing
}

// newStandingListCmd builds `approval standing list`.
func newStandingListCmd(deps approvalDeps) *cobra.Command {
	var params policy.StandingListParams
	var kind, id string
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List the standing grants one subject holds",
		Long:    "List the standing grants one subject holds, ordered by storage key.",
		Example: "  cascade approval standing list --subject-kind user --subject-id ada",
		Args:    usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := approvalClient(cmd.Context(), deps, "approval standing list")
			if err != nil {
				return err
			}
			params.Subject = policy.Subject{Kind: policy.SubjectKind(kind), ID: id}
			res, err := c.StandingGrantList(cmd.Context(), params)
			if err != nil {
				return err
			}
			return approvalOutputWriter(cmd).Result(standingListView(res))
		},
	}
	addSubjectFlags(cmd, &kind, &id)
	return cmd
}

// standingListView renders the grants a subject holds.
type standingListView policy.StandingListResult

// String names each grant's capability and expiry.
func (v standingListView) String() string {
	if len(v.Grants) == 0 {
		return "this subject holds no standing grants"
	}
	out := ""
	for i, g := range v.Grants {
		if i > 0 {
			out += "\n"
		}
		out += fmt.Sprintf("%s expires %s", g.Capability, g.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return out
}

// newStandingWriteCmd builds `approval standing create` and `... change`.
// Both take the same terms and run the same guarded write, so they are
// built from one function rather than two that could drift.
func newStandingWriteCmd(deps approvalDeps, verb string) *cobra.Command {
	var params policy.StandingWriteParams
	var kind, id string
	var actionClass uint8
	var ttl time.Duration
	cmd := &cobra.Command{
		Use:   verb,
		Short: "Write the terms of a standing grant (" + verb + ")",
		Long: "Write a standing grant's terms. This is an elevated verb: it needs a fresh\n" +
			"local attestation. A deny-listed action class or an elevation-class verb is\n" +
			"refused before any row is written.",
		Example: "  cascade approval standing " + verb +
			" --grant-id 01J8ZC5W2K4F6H8M0P2R4T6V8X --action workspace.write --capability fs.write",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := refuseElevatedOnUnsupportedPlatform("approval standing " + verb); err != nil {
				return err
			}
			c, err := approvalClient(cmd.Context(), deps, "approval standing "+verb)
			if err != nil {
				return err
			}
			params.Grantee = policy.Subject{Kind: policy.SubjectKind(kind), ID: id}
			params.ActionClass = policy.ActionClass(actionClass)
			params.Exp = deps.Clock.Now().Add(ttl)
			res, err := standingWriteCall(cmd, c, verb, params)
			if err != nil {
				return err
			}
			return approvalOutputWriter(cmd).Result(standingWriteView(res))
		},
	}
	cmd.Flags().StringVar(&params.GrantID, "grant-id", "", "the grant's identifier")
	cmd.Flags().StringVar(&params.Action, "action", "", "the verb the grant covers")
	cmd.Flags().StringVar(&params.Capability, "capability", "", "the registered capability the row is stored under")
	cmd.Flags().StringVar(&params.Scope, "scope", "", "narrow the grant, for example to one repository path")
	cmd.Flags().Uint8Var(&actionClass, "action-class", 0, "the action class, as its enum value")
	cmd.Flags().DurationVar(&ttl, "ttl", 24*time.Hour, "how long the grant applies for")
	addSubjectFlags(cmd, &kind, &id)
	return cmd
}

// standingWriteCall dispatches to the create or the change wrapper. The
// verb string reached here from newStandingWriteCmd's own construction, so
// an unknown value is a programming error and is refused rather than
// defaulted to the creating call.
func standingWriteCall(cmd *cobra.Command, c standingWriter, verb string,
	params policy.StandingWriteParams) (policy.StandingWriteResult, error) {
	switch verb {
	case "create":
		return c.StandingGrantCreate(cmd.Context(), params)
	case "change":
		return c.StandingGrantChange(cmd.Context(), params)
	default:
		return policy.StandingWriteResult{}, cascade.Newf(cascade.KindInvalidInput,
			"cascade approval standing: %q is not a standing-grant write verb", verb)
	}
}

// standingWriter is the narrow slice of the client SDK the write verbs
// use. It is an interface so the command can be driven in a test without a
// socket.
type standingWriter interface {
	StandingGrantCreate(ctx context.Context, params policy.StandingWriteParams) (policy.StandingWriteResult, error)
	StandingGrantChange(ctx context.Context, params policy.StandingWriteParams) (policy.StandingWriteResult, error)
}

// standingWriteView reports what a standing-grant write changed.
type standingWriteView policy.StandingWriteResult

// String names the row that was written.
func (v standingWriteView) String() string {
	if !v.Changed {
		return "no standing grant was written"
	}
	return fmt.Sprintf("standing grant %s now covers %s", v.GrantID, v.Action)
}

// newStandingRevokeCmd builds `approval standing revoke`.
func newStandingRevokeCmd(deps approvalDeps) *cobra.Command {
	var params policy.StandingRevokeParams
	var kind, id string
	cmd := &cobra.Command{
		Use:     "revoke",
		Short:   "Remove one standing grant",
		Long:    "Remove one standing grant. It takes effect on the next evaluation.",
		Example: "  cascade approval standing revoke --subject-kind user --subject-id ada --capability fs.write",
		Args:    usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := approvalClient(cmd.Context(), deps, "approval standing revoke")
			if err != nil {
				return err
			}
			params.Grantee = policy.Subject{Kind: policy.SubjectKind(kind), ID: id}
			res, err := c.StandingGrantRevoke(cmd.Context(), params)
			if err != nil {
				return err
			}
			return approvalOutputWriter(cmd).Result(revokeView(res))
		},
	}
	cmd.Flags().StringVar(&params.Capability, "capability", "", "the capability the grant is stored under")
	addSubjectFlags(cmd, &kind, &id)
	return cmd
}

// revokeView reports a revocation.
type revokeView policy.StandingWriteResult

// String names what was revoked.
func (v revokeView) String() string {
	return fmt.Sprintf("revoked the standing grant on %s", v.Action)
}

// addSubjectFlags attaches the two flags every subject-addressed verb
// takes, so the flag names cannot drift between them.
func addSubjectFlags(cmd *cobra.Command, kind, id *string) {
	cmd.Flags().StringVar(kind, "subject-kind", "", "the subject's kind, for example user or agent")
	cmd.Flags().StringVar(id, "subject-id", "", "the subject's identifier")
}
