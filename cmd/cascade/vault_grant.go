// Purpose: `cascade vault grant|grants|revoke` — how a human issues,
//
//	inspects and withdraws the standing grants the headless daemon reads
//	provider credentials under (R-14.243).
//
// Inputs: the same vaultDeps every other vault verb uses, so grants are
//
//	issued through the SAME production ElevationGate as `vault get`. There
//	is deliberately no second way to prove presence.
//
// Outputs: the issued grant's id and expiry; the register listing; a
//
//	revocation.
//
// Constraints: issuing is ELEVATED. A grant is permission to read a secret
//
//	without a human present, so minting one must cost exactly what reading
//	one costs. Listing and revoking are not elevated: neither discloses a
//	value, and a revoke that required elevation would be a revoke an
//	operator could be locked out of.
//
// SPORT: cli.vault.grant/ADD — P1-E10-W4-S87-T1.
package main

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// vaultGrants opens the grant register for one command invocation.
func vaultGrants(deps vaultDeps) (*secrets.Grants, error) {
	if deps.Paths == nil {
		return nil, cascade.New(cascade.KindUnavailable,
			"vault: could not resolve the cascade data directory")
	}
	dir := deps.Paths.DataDir()
	if dir == "" {
		return nil, cascade.New(cascade.KindUnavailable,
			"vault: could not resolve the cascade data directory")
	}
	store, err := secrets.NewFileGrantStore(dir)
	if err != nil {
		return nil, err
	}
	return secrets.NewGrants(store, runtime.NewSystemClock())
}

// newVaultGrantCmd issues a grant.
func newVaultGrantCmd(deps vaultDeps) *cobra.Command {
	var ttl time.Duration
	cmd := &cobra.Command{
		Use:   "grant <name>",
		Short: "Authorise the daemon to read one secret without a human present",
		Long: "Issue a standing grant for ONE secret and ONE verb (vault get), for a\n" +
			"bounded time. The daemon reads provider credentials under these grants;\n" +
			"without one, every key-authenticated provider fails closed.\n\n" +
			"A grant opens the named secret and nothing else. It expires on its own,\n" +
			"can be revoked at any time (effective on the next read, not the next\n" +
			"restart), and every use is recorded. Issuing one is elevated, exactly\n" +
			"like reading the secret yourself.",
		Example: "  # Let the daemon dispatch through a provider you added:\n" +
			"  cascade vault grant provider.compatsub.key\n" +
			"  cascade vault grant provider.compatsub.key --ttl 24h",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVaultGrant(cmd, deps, args[0], ttl)
		},
	}
	cmd.Flags().DurationVar(&ttl, "ttl", secrets.DefaultGrantTTL,
		"how long the grant lives (clamped to "+secrets.MaxGrantTTL.String()+")")
	return cmd
}

// runVaultGrant proves presence, then issues.
func runVaultGrant(cmd *cobra.Command, deps vaultDeps, name string, ttl time.Duration) error {
	// The elevation gate runs FIRST, and on the same verb the read itself
	// uses: minting permission to read must cost what reading costs.
	if deps.Gate == nil {
		return cascade.New(cascade.KindElevationRequired,
			"vault grant: no elevation gate is configured; refusing to issue a grant")
	}
	if err := deps.Gate.Authorize(cmd.Context(), secrets.VerbGet); err != nil {
		return err
	}
	grants, err := vaultGrants(deps)
	if err != nil {
		return err
	}
	grant, err := grants.Issue(cmd.Context(), name, ttl)
	if err != nil {
		return err
	}
	out := vaultOutputWriter(cmd)
	return out.Result(vaultGrantView{
		ID: grant.ID, KeyRef: grant.KeyRef, Verb: grant.Verb,
		ExpiresAt: grant.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// vaultGrantView is one grant as reported. It carries no value and no
// path. Named for its namespace because approval_standing.go already owns
// the bare grantView for a different domain.
type vaultGrantView struct {
	ID        string `json:"id"`
	KeyRef    string `json:"key_ref"`
	Verb      string `json:"verb"`
	ExpiresAt string `json:"expires_at"`
	Revoked   bool   `json:"revoked,omitempty"`
}

// newVaultGrantsCmd lists the register.
func newVaultGrantsCmd(deps vaultDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "grants",
		Short: "List the standing grants on this machine",
		Long: "List every grant, including expired and revoked ones. Nothing here is a\n" +
			"secret: a grant records WHICH name may be read and until when, never a\n" +
			"value. Listing is not elevated.",
		Example: "  cascade vault grants\n" +
			"  cascade vault grants --json",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			grants, err := vaultGrants(deps)
			if err != nil {
				return err
			}
			records, err := grants.List(cmd.Context())
			if err != nil {
				return err
			}
			views := make(vaultGrantList, 0, len(records))
			for _, g := range records {
				views = append(views, vaultGrantView{
					ID: g.ID, KeyRef: g.KeyRef, Verb: g.Verb,
					ExpiresAt: g.ExpiresAt.UTC().Format(time.RFC3339), Revoked: g.Revoked,
				})
			}
			return vaultOutputWriter(cmd).Result(views)
		},
	}
}

// newVaultRevokeCmd withdraws one grant.
func newVaultRevokeCmd(deps vaultDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <grant-id>",
		Short: "Withdraw a standing grant",
		Long: "Withdraw a grant by id (see `cascade vault grants`). It takes effect on\n" +
			"the daemon's NEXT read, not at its next restart.\n\n" +
			"Revoking is deliberately not elevated: it only ever removes authority,\n" +
			"and a revoke an operator could be locked out of would be worse than no\n" +
			"revoke at all.",
		Example: "  cascade vault grants          # find the id\n" +
			"  cascade vault revoke 3f9a1c0b2d4e5f60",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			grants, err := vaultGrants(deps)
			if err != nil {
				return err
			}
			return grants.Revoke(cmd.Context(), args[0])
		},
	}
}

// String renders one grant as a line of text. Without it the writer falls
// back to Go's default struct formatting and prints `{id key verb expiry
// false}` — the same raw-struct output defect the W-3 gate recorded against
// `vault list` and `recall index rebuild`.
func (v vaultGrantView) String() string {
	state := "live"
	if v.Revoked {
		state = "revoked"
	}
	return v.ID + "  " + v.KeyRef + "  " + v.Verb + "  expires " + v.ExpiresAt + "  " + state
}

// vaultGrantList renders the register as one line per grant, so the list
// command prints text rather than a Go slice literal.
type vaultGrantList []vaultGrantView

// String renders every grant, or says plainly that there are none.
func (l vaultGrantList) String() string {
	if len(l) == 0 {
		return "no standing grants on this machine"
	}
	out := ""
	for i, v := range l {
		if i > 0 {
			out += "\n"
		}
		out += v.String()
	}
	return out
}
