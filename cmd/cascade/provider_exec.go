// Purpose: `cascade provider exec` (P1-GWY-10a, contract:gateway-operator-exec):
//   launch an operator (T0) harness on a gateway account, stripping every
//   gateway credential variable from the environment first. Without an
//   account it strips the host-injected gateway variables (HostInjectedVars);
//   with an account it also strips the competing Claude credential variables
//   and injects the account's gateway variables and token.
// Inputs: cobra args; an optional account name; a command after --.
// Outputs: exec in place on unix; refusal on windows or errors.
// Constraints: -- required; command resolved with exec.LookPath; unknown
//   account KindNotFound; helper errors unchanged (ErrHelperChanged etc.).
// SPORT: cli.provider.exec/ADD (P1-GWY-10a, contract:gateway-operator-exec).

package main

import (
	"os"
	"os/exec"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/spf13/cobra"
)

// HostInjectedVars returns the list of environment variable names that the
// host injects for gateway credential management. These are stripped from
// the environment before exec.
func HostInjectedVars() []string {
	return []string{
		"CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_BASE_URL",
	}
}

// newProviderExecCmd creates the `provider exec` subcommand.
func newProviderExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec [<account>] -- <command> [args...]",
		Short: "Launch a harness on a gateway account",
		Long: "Launch an operator harness on a gateway account.\n\n" +
			"Without an account, strips the host-injected gateway variables and\n" +
			"execs the command in place. With an account, also strips the competing\n" +
			"Claude credential variables and injects the account's gateway variables\n" +
			"and token. The command is resolved with exec.LookPath on the inherited PATH.\n\n" +
			"The `--` separator is required to distinguish the account from the command.",
		Example: "  cascade provider exec -- claude\n" +
			"  cascade provider exec myaccount -- claude",
		Args:               cobra.MinimumNArgs(0),
		Annotations:        map[string]string{"local": "true"},
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProviderExec(cmd, args)
		},
	}
	return cmd
}

// runProviderExec parses the arguments, scrubs the environment, and execs
// the command. On unix it replaces the process; on windows it refuses.
func runProviderExec(_ *cobra.Command, args []string) error {
	// Parse arguments: [<account>] -- <command> [args...]
	account, cmdArgs, err := parseExecArgs(args)
	if err != nil {
		return err
	}

	if len(cmdArgs) == 0 {
		return cascade.New(cascade.KindInvalidInput, "provider exec: no command specified after --")
	}

	// Build the scrubbed environment
	env := scrubExecEnv(account)

	// Resolve the command path
	cmdPath, err := exec.LookPath(cmdArgs[0])
	if err != nil {
		return cascade.Wrap(cascade.KindNotFound, err, "provider exec: command not found")
	}

	// Platform-specific exec
	return platformExec(cmdPath, cmdArgs, env)
}

// parseExecArgs splits the arguments into account (optional) and command.
// The -- separator is required.
func parseExecArgs(args []string) (account string, cmdArgs []string, err error) {
	// Find --
	sepIdx := -1
	for i, arg := range args {
		if arg == "--" {
			sepIdx = i
			break
		}
	}

	if sepIdx < 0 {
		return "", nil, cascade.New(cascade.KindInvalidInput, "provider exec: -- required to separate account from command")
	}

	// Everything before -- is the account (0 or 1 args)
	if sepIdx > 1 {
		return "", nil, cascade.New(cascade.KindInvalidInput, "provider exec: too many arguments before --")
	}
	if sepIdx == 1 {
		account = args[0]
	}

	// Everything after -- is the command
	cmdArgs = args[sepIdx+1:]
	return account, cmdArgs, nil
}

// scrubExecEnv builds the environment for the exec'd process. Without an
// account it strips HostInjectedVars. With an account it also strips the
// competing Claude credential variables.
func scrubExecEnv(account string) []string {
	env := os.Environ()

	// Strip host-injected vars
	for _, name := range HostInjectedVars() {
		env = removeEnvVar(env, name)
	}

	// With account, also strip competing Claude credential variables
	if account != "" {
		env = removeEnvVar(env, "ANTHROPIC_API_KEY")
		env = removeEnvVar(env, "CLAUDE_CODE_OAUTH_TOKEN")
	}

	return env
}

// removeEnvVar removes all instances of name=value from the environment slice.
func removeEnvVar(env []string, name string) []string {
	prefix := name + "="
	result := make([]string, 0, len(env))
	for _, e := range env {
		if len(e) >= len(prefix) && e[:len(prefix)] == prefix {
			continue
		}
		result = append(result, e)
	}
	return result
}
