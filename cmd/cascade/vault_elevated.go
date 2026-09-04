// Purpose: `cascade vault get` and `cascade vault rotate`, the two
//
//	elevated vault verbs, plus the production elevation gate the broker
//	consults before either one touches the store.
//
// Inputs: cobra args/flags, the shared vaultDeps, and (for the gate) the
//
//	platform elevation keystore and trust record.
//
// Outputs: `get` writes the raw value to stdout and nothing else, so a
//
//	caller can pipe it without stripping a banner; `rotate` reports the
//	name it replaced. On refusal both emit ELEVATION_REQUIRED with the
//	taxonomy's elevation-required exit status.
//
// Constraints: the gate decides, this file does not. It defers to
//
//	internal/policy, which classifies the verb through the one canonical
//	elevated-verb table, so there is no second copy of that table here. A
//	gate that cannot prove local presence refuses.
//
// SPORT: cmd/cascade/vault (ADD - get, rotate).

package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// elevationGate is the production secrets.ElevationGate. It resolves the
// two daemonless-elevation preconditions from internal/elevation (is a
// device key enrolled, is a local authenticator usable) and hands them to
// internal/policy for the decision.
//
// Both dependencies are constructed lazily, per invocation: building the
// command tree must never probe the platform keystore.
type elevationGate struct {
	newKeystore     func() elevation.ElevationKeystore
	newTrustBackend func() elevation.Backend
	clock           elevation.Clock
	getenv          runtime.Getenv
}

// newElevationGate builds the production gate.
func newElevationGate(
	newKeystore func() elevation.ElevationKeystore,
	newTrustBackend func() elevation.Backend,
	clock elevation.Clock,
	getenv runtime.Getenv,
) *elevationGate {
	return &elevationGate{
		newKeystore: newKeystore, newTrustBackend: newTrustBackend, clock: clock, getenv: getenv,
	}
}

// Authorize implements secrets.ElevationGate. Every failure path refuses:
// a keystore that will not construct, a trust store that will not read, and
// CASCADE_NO_INPUT=1 (which forbids the local-presence prompt the flow
// needs) are all "cannot prove local presence", never "proceed".
func (g *elevationGate) Authorize(_ context.Context, verb string) error {
	if g.getenv != nil && g.getenv("CASCADE_NO_INPUT") == "1" {
		return cascade.Newf(cascade.KindElevationRequired,
			"vault: %s needs local presence and CASCADE_NO_INPUT=1 forbids prompting for it", verb)
	}
	enrolled, available := g.preconditions()
	if !policy.IsDaemonlessElevationAllowed(verb, enrolled, available) {
		return policy.ErrElevationRequired(verb, enrolled, available)
	}
	return nil
}

// preconditions resolves the two hardware/enrollment facts. A dependency
// this gate cannot construct reports false, so the policy layer refuses.
func (g *elevationGate) preconditions() (enrolled, available bool) {
	if g.newKeystore != nil {
		if ks := g.newKeystore(); ks != nil {
			available = ks.IsAvailable()
		}
	}
	if g.newTrustBackend != nil {
		if backend := g.newTrustBackend(); backend != nil {
			enrolled = elevation.NewElevationTrustStore(backend, g.clock).IsEnrolled()
		}
	}
	return enrolled, available
}

func newVaultGetCmd(deps vaultDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "get NAME",
		Short: "Read a secret's value (elevated: requires local presence)",
		Long: "Print the value stored under NAME.\n\n" +
			"This is an elevated verb. It requires an enrolled elevation helper and a\n" +
			"working local authenticator, and it is excluded from the MCP surface: no\n" +
			"agent can reach a secret value through a tool call. Without an elevation\n" +
			"session the command exits with ELEVATION_REQUIRED and reads nothing.\n\n" +
			"The value is written to stdout with no trailing newline and no banner, so\n" +
			"it can be piped directly.",
		Example:     "  cascade vault get API_TOKEN\n  cascade vault get API_TOKEN | tr -d '\\n' | pbcopy",
		Args:        usageArgs(cobra.ExactArgs(1)),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			broker, err := vaultBroker(deps)
			if err != nil {
				return err
			}
			value, err := broker.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			// Written straight to the command's stdout, not through the
			// output writer's Result: a secret must not be wrapped in a
			// JSON envelope that a shell pipeline would then have to
			// parse, and must not be rendered by a formatter that could
			// quote or truncate it.
			_, werr := cmd.OutOrStdout().Write(value)
			if werr != nil {
				return cascade.Wrap(cascade.KindInternal, werr, "vault: writing the value failed")
			}
			return nil
		},
	}
}

// rotateResultView is `vault rotate`'s rendered result: the name, never the
// old or the new value.
type rotateResultView struct {
	Name    string `json:"name"`
	Rotated bool   `json:"rotated"`
	Backend string `json:"backend"`
}

func newVaultRotateCmd(deps vaultDeps) *cobra.Command {
	var valueFile string
	cmd := &cobra.Command{
		Use:   "rotate NAME",
		Short: "Replace an existing secret's value (elevated: requires local presence)",
		Long: "Replace the value stored under NAME, reading the new value from stdin or\n" +
			"from the file named by --value-file.\n\n" +
			"This is an elevated verb: it destroys the previous value, so it carries\n" +
			"the same authorisation as reading one. A NAME that is not stored is an\n" +
			"error, not a silent create, so a typo cannot quietly mint a new secret.",
		Example: "  cascade vault rotate API_TOKEN < new-token.txt\n" +
			"  cascade vault rotate API_TOKEN --value-file ./new-token.txt",
		Args:        usageArgs(cobra.ExactArgs(1)),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			broker, err := vaultBroker(deps)
			if err != nil {
				return err
			}
			value, err := readSecretValue(cmd, deps, valueFile)
			if err != nil {
				return err
			}
			if err := broker.Rotate(cmd.Context(), args[0], value); err != nil {
				return err
			}
			return vaultOutputWriter(cmd).Result(rotateResultView{
				Name: args[0], Rotated: true, Backend: broker.Backend(),
			})
		},
	}
	cmd.Flags().StringVar(&valueFile, "value-file", "", "read the new value from this file instead of stdin")
	return cmd
}
