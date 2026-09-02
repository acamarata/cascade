// Purpose: `cascade completion [bash|zsh|fish|powershell]` — shell completion
//
//	script generation.
//
// Inputs:  the shell name argument; the root command whose flag/command tree
//
//	is introspected to generate the script.
//
// Outputs: the completion script for the requested shell, written to stdout.
// Constraints: delegates entirely to cobra's built-in Gen*Completion helpers
//
//	(no hand-written completion logic); the default `completion` command
//	cobra would otherwise auto-register is disabled in favor of this one so
//	help output stays stable across cobra versions.
//
// SPORT: cmd/cascade — cobra-root, global-flags, version, completions.
package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var completionShells = []string{"bash", "zsh", "fish", "powershell"}

// newCompletionCmd builds the `completion` subcommand against root, the same
// root command instance whose flags/subcommands the generated scripts must
// describe.
func newCompletionCmd(root *cobra.Command) *cobra.Command {
	root.CompletionOptions.DisableDefaultCmd = true

	cmd := &cobra.Command{
		Use:                   "completion [bash|zsh|fish|powershell]",
		Short:                 "Generate shell completion scripts",
		Long:                  "Generate a shell completion script for cascade.\n\nSource it in your shell's startup file, e.g.:\n\n  bash:       source <(cascade completion bash)\n  zsh:        source <(cascade completion zsh)\n  fish:       cascade completion fish | source\n  powershell: cascade completion powershell | Out-String | Invoke-Expression",
		DisableFlagsInUseLine: true,
		ValidArgs:             completionShells,
		Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			switch args[0] {
			case "bash":
				return root.GenBashCompletionV2(out, true)
			case "zsh":
				return root.GenZshCompletion(out)
			case "fish":
				return root.GenFishCompletion(out, true)
			case "powershell":
				return root.GenPowerShellCompletionWithDesc(out)
			default:
				return fmt.Errorf("unsupported shell %q", args[0])
			}
		},
	}
	return cmd
}
