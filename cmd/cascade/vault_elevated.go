// Purpose: `cascade vault get` and `cascade vault rotate`, the two
//
//	elevated vault verbs.
//
// Inputs: cobra args/flags and the shared vaultDeps, whose Gate is the
//
//	signed local elevation gate (vault_elevated_gate.go).
//
// Outputs: `get` writes the raw value to stdout and nothing else, so a
//
//	caller can pipe it without stripping a banner; `rotate` reports the
//	name it replaced. On refusal both emit ELEVATION_REQUIRED with the
//	taxonomy's elevation-required exit status.
//
// Constraints: the gate decides, this file does not. A gate that cannot
//
//	prove local presence refuses, and `get` releases a value only after the
//	gate recorded the access (name and verb, never the value).
//
// SPORT: cmd/cascade/vault (ADD - get, rotate).

package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// vaultAccessRecorder is the gate's audit of a released value. The
// production elevationGate implements it; a gate that cannot record an
// access cannot release a value.
type vaultAccessRecorder interface {
	RecordVaultAccess(ctx context.Context, name, verb string) error
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
			recorder, ok := deps.Gate.(vaultAccessRecorder)
			if !ok {
				return cascade.New(cascade.KindElevationRequired,
					"vault get: the elevation gate cannot audit an access; refusing to read")
			}
			// The access is recorded first, through the gate's audit sink,
			// which reads no vault and no custody; a failed record reads
			// nothing and is reported on stderr with its reason code, so a
			// refusal whose audit sink is down is never silent.
			if err := recorder.RecordVaultAccess(cmd.Context(), args[0], secrets.VerbGet); err != nil {
				if _, werr := fmt.Fprintln(cmd.ErrOrStderr(), "vault get: refused (audit_unavailable): the access could not be recorded"); werr != nil {
					return cascade.Wrap(cascade.KindUnavailable, werr, "vault: writing the refusal failed")
				}
				return err
			}
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
