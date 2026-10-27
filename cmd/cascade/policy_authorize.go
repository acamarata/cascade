// Purpose: `cascade policy authorize <action>`, the one way an operator opens
//
//	a same-turn window for an action the deny list otherwise refuses. It
//	sends the always-elevated policy.authorize RPC through callElevated, so
//	the daemon records the authorization only after a fresh local
//	attestation for exactly this call.
//
// Inputs: the exact action text (positional), the approvalDeps socket seams
//
//	and the elevated-call seams (policyAuthorizeCallDeps).
//
// Outputs: the action, the authorization nonce and the instant the window
//
//	closes, through internal/output.Writer.
//
// Constraints: untagged so the command tree is the same on every OS; on
//
//	Windows the verb refuses before dialing. AUTHORIZE text typed into a chat
//	turn is never read: this verb is the only producer of an authorization.
//
// SPORT: cli/policy authorize (ADD) -- P1-SEC-10.
package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

// policyAuthorizeMethod is the always-elevated, owner-only daemon verb that
// records one same-turn authorization.
const policyAuthorizeMethod = "policy.authorize"

// policyAuthorizeParams is the verb's only parameter: the exact action text.
type policyAuthorizeParams struct {
	Action string `json:"action"`
}

// policyAuthorizeResult is what the daemon recorded: exactly
// {"nonce", "expires_at"}.
type policyAuthorizeResult struct {
	Nonce     string    `json:"nonce"`
	ExpiresAt time.Time `json:"expires_at"`
}

// policyAuthorizeCallDeps builds the elevated-call collaborators the verb
// signs its attestation with; tests inject a test custody here.
var policyAuthorizeCallDeps = productionElevatedCallDeps

// newPolicyAuthorizeCmd builds `policy authorize <action>`.
func newPolicyAuthorizeCmd(deps approvalDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "authorize <action>",
		Short: "Authorize one deny-listed action for a single use in this turn",
		Long: "Open a same-turn window for one exact action the deny list refuses. The\n" +
			"daemon needs a fresh local attestation for this call. The window answers one\n" +
			"evaluation by the MCP subject, closes after two minutes, and ends when the next\n" +
			"authorization starts. Elevation-class verbs and actions the policy refuses\n" +
			"unconditionally cannot be authorized. AUTHORIZE text in a chat is never read.",
		Example: `  cascade policy authorize "git push origin main"`,
		Args:    usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseElevatedOnUnsupportedPlatform("policy authorize"); err != nil {
				return err
			}
			c, err := approvalClient(cmd.Context(), deps, "policy authorize")
			if err != nil {
				return err
			}
			var res policyAuthorizeResult
			params := policyAuthorizeParams{Action: args[0]}
			err = callElevated(cmd.Context(), c, policyAuthorizeCallDeps(cmd.ErrOrStderr()), policyAuthorizeMethod, params, &res)
			if err != nil {
				return err
			}
			view := policyAuthorizeView{Action: args[0], Nonce: res.Nonce, ExpiresAt: res.ExpiresAt}
			return approvalOutputWriter(cmd).Result(view)
		},
	}
}

// policyAuthorizeView renders one recorded authorization: the action the CLI
// sent plus the daemon's reply, in text and under --json alike.
type policyAuthorizeView struct {
	Action    string    `json:"action"`
	Nonce     string    `json:"nonce"`
	ExpiresAt time.Time `json:"expires_at"`
}

// String names the action, the nonce and when the window closes.
func (v policyAuthorizeView) String() string {
	return fmt.Sprintf("authorized %q once (nonce %s), until %s",
		v.Action, v.Nonce, v.ExpiresAt.UTC().Format(time.RFC3339))
}
