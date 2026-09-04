// Purpose: `cascade vault set` and its name-collision flow, split from
//
//	vault.go under the repo's 300-line file cap. Same responsibility
//	boundary: this file owns writing a NEW secret, nothing else.
//
// Inputs: cobra args/flags and the shared vaultDeps.
// Outputs: the name the value landed under, through
//
//	internal/output.Writer. Never the value.
//
// Constraints: the value is read from stdin or --value-file, never from
//
//	argv. A collision prompt that gets an unrecognised answer refuses and
//	writes nothing, rather than guessing which of "update in place" and
//	"save alongside" the operator meant.
//
// SPORT: cmd/cascade/vault (ADD - set).

package main

import (
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// setResultView is the rendered result of `vault set`. It reports the NAME
// the value landed under and never the value.
type setResultView struct {
	Name     string `json:"name"`
	Replaced bool   `json:"replaced"`
	Backend  string `json:"backend"`
}

func newVaultSetCmd(deps vaultDeps) *cobra.Command {
	var valueFile string
	var rename bool
	var fromQuarantine string
	cmd := &cobra.Command{
		Use:   "set NAME",
		Short: "Store a secret, reading its value from stdin",
		Long: "Store a secret under NAME.\n\n" +
			"The value is read from stdin, or from the file named by --value-file. It\n" +
			"is never taken from the command line, where it would be recorded in shell\n" +
			"history and visible in the process table.\n\n" +
			"If NAME already exists, an interactive run asks whether to update it in\n" +
			"place or save the new value as NAME_2. Under CASCADE_NO_INPUT=1 the value\n" +
			"updates in place silently, matching `vault import`'s overwrite behaviour;\n" +
			"pass --rename to save under the suffixed name instead.\n\n" +
			"--from-quarantine ID promotes a detection the secret detector recorded:\n" +
			"the NAME comes from the entry's suggested name, and the value is\n" +
			"re-supplied here, because the detector never stored one. Under\n" +
			"CASCADE_NO_INPUT=1 the value must be piped in or named by --value-file.",
		Example: "  cascade vault set API_TOKEN < token.txt\n" +
			"  printf %s \"$TOKEN\" | cascade vault set API_TOKEN\n" +
			"  cascade vault set API_TOKEN --value-file ./token.txt --rename\n" +
			"  printf %s \"$TOKEN\" | cascade vault set --from-quarantine 4f3a2b1c9d8e7f60",
		Args:        usageArgs(cobra.MaximumNArgs(1)),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if fromQuarantine != "" {
				if len(args) != 0 {
					return cascade.New(cascade.KindInvalidInput,
						"vault: --from-quarantine takes the NAME from the quarantine entry; do not also pass one")
				}
				return runVaultPromote(cmd, deps, fromQuarantine, valueFile)
			}
			if len(args) != 1 {
				return cascade.New(cascade.KindInvalidInput, "vault: set needs a NAME")
			}
			return runVaultSet(cmd, deps, args[0], valueFile, rename)
		},
	}
	cmd.Flags().StringVar(&valueFile, "value-file", "", "read the value from this file instead of stdin")
	cmd.Flags().BoolVar(&rename, "rename", false, "on a name collision, save as NAME_2 instead of updating in place")
	cmd.Flags().StringVar(&fromQuarantine, "from-quarantine", "",
		"promote the quarantine entry with this id: its suggested name becomes NAME")
	return cmd
}

func runVaultSet(cmd *cobra.Command, deps vaultDeps, name, valueFile string, rename bool) error {
	broker, err := vaultBroker(deps)
	if err != nil {
		return err
	}
	// The collision question is asked BEFORE the value is read: when the
	// value comes from stdin, reading it first would consume the answer
	// too, and the prompt would see an empty line every time.
	mode, err := resolveSetMode(cmd, deps, broker, name, rename)
	if err != nil {
		return err
	}
	value, err := readSecretValue(cmd, deps, valueFile)
	if err != nil {
		return err
	}
	result, err := broker.Set(cmd.Context(), name, value, mode)
	if err != nil {
		return err
	}
	return vaultOutputWriter(cmd).Result(setResultView{
		Name: result.Name, Replaced: result.Replaced, Backend: broker.Backend(),
	})
}

// resolveSetMode implements the name-collision flow. --rename is explicit
// and wins outright; otherwise an interactive session is asked, and a
// non-interactive one updates in place.
func resolveSetMode(cmd *cobra.Command, deps vaultDeps, broker *secrets.Broker, name string, rename bool) (secrets.SetMode, error) {
	if rename {
		return secrets.SetRename, nil
	}
	exists, err := broker.Exists(cmd.Context(), name)
	if err != nil {
		return secrets.SetUpdate, err
	}
	if !exists || noInput(deps) {
		return secrets.SetUpdate, nil
	}
	return promptCollision(cmd, name)
}

// promptCollision asks the operator what to do about an existing name. An
// answer it does not recognise is a refusal, not a guessed default: this
// call decides whether an existing secret is destroyed.
func promptCollision(cmd *cobra.Command, name string) (secrets.SetMode, error) {
	w := vaultOutputWriter(cmd)
	w.Warn("%s exists - update it, or save as %s_2? [update/rename]", name, name)
	answer, err := readLine(cmd.InOrStdin())
	if err != nil {
		return secrets.SetUpdate, cascade.Wrap(cascade.KindInvalidInput, err, "vault: could not read the answer")
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "update", "u":
		return secrets.SetUpdate, nil
	case "rename", "r", name + "_2":
		return secrets.SetRename, nil
	default:
		return secrets.SetUpdate, cascade.Newf(cascade.KindInvalidInput,
			"vault: answer %q is neither \"update\" nor \"rename\"; nothing was written", strings.TrimSpace(answer))
	}
}

// readLine reads one line from r without consuming more than it needs.
func readLine(r io.Reader) (string, error) {
	var line []byte
	buf := make([]byte, 1)
	for len(line) < 64 {
		n, err := r.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				break
			}
			line = append(line, buf[0])
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", err
		}
	}
	return string(line), nil
}
