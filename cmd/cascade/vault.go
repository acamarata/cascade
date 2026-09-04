// Purpose: `cascade vault` (07-CLI-COMMAND-TREE §vault) - the noun root
//
//	plus the two non-elevated verbs, set and list. get and rotate live in
//	vault_elevated.go, import in vault_import.go, audit in
//	vault_audit.go, so no file carries more than one responsibility.
//
// Inputs: cobra args/flags; a vaultDeps injected at construction so no
//
//	test touches the real keychain, the real vault directory, or the real
//	environment.
//
// Outputs: process output through internal/output.Writer. `list` emits
//
//	NAMES ONLY: there is no code path from this file to a stored value
//	except `get`, which lives behind the elevation gate.
//
// Constraints: a secret value is never accepted as a command-line
//
//	argument (it would land in the shell history and the process table) -
//	it is read from stdin or from a file named by --value-file. Nothing
//	here logs a value, and no error message carries one.
//
// SPORT: cmd/cascade/vault (ADD - set, list; get, rotate, import, audit in
//
//	the sibling files).

package main

import (
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// vaultService is the keychain service / secret-service collection label
// every cascade vault entry is filed under.
const vaultService = "cascade-vault"

// maxSecretValueBytes bounds a value read from stdin or a file. A vault
// holds credentials, not payloads; without a bound, `cascade vault set X <
// /dev/zero` fills memory.
const maxSecretValueBytes = 1 << 20

// vaultDeps carries every external input the vault command tree needs.
type vaultDeps struct {
	// Paths resolves the data directory the encrypted file vault lives in.
	Paths runtime.PathProvider
	// Getenv reads the process environment (CASCADE_NO_INPUT).
	Getenv runtime.Getenv
	// NewCustody selects the custody backend. Injected so tests run
	// against a temp-dir file vault and never the user's real keychain.
	NewCustody func() (secrets.Custody, error)
	// Gate authorises the elevated verbs. Nil is a refusal, not a pass:
	// see secrets.Broker's authorize.
	Gate secrets.ElevationGate
	// ReadFile loads a --value-file / import file.
	ReadFile func(string) ([]byte, error)
}

// productionVaultDeps builds vaultDeps against the real environment.
func productionVaultDeps() vaultDeps {
	paths := lazyPaths{}
	getenv := os.Getenv
	return vaultDeps{
		Paths:  paths,
		Getenv: getenv,
		NewCustody: func() (secrets.Custody, error) {
			dir := paths.get(func(p runtime.PathProvider) string { return p.DataDir() })
			if dir == "" {
				return nil, cascade.New(cascade.KindUnavailable,
					"vault: could not resolve the cascade data directory")
			}
			return secrets.SelectCustody(secrets.Config{Service: vaultService, Dir: dir})
		},
		Gate: newElevationGate(
			elevation.NewKeystore,
			func() elevation.Backend {
				return elevation.NewFileBackend(paths.get(func(p runtime.PathProvider) string { return p.DataDir() }))
			},
			runtime.NewSystemClock(),
			getenv,
		),
		ReadFile: os.ReadFile,
	}
}

// mountVaultCmd attaches the `vault` command tree to the root, following
// mountDaemonCmd's pattern: dependencies are resolved lazily, so building
// the command tree never touches the environment.
func mountVaultCmd(root *cobra.Command) {
	cmd := newVaultCmd(productionVaultDeps())
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

// newVaultCmd builds the vault noun and mounts every verb.
func newVaultCmd(deps vaultDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vault",
		Short: "Store and retrieve secrets in the platform keychain",
		Long: "Manage cascade's secret vault.\n\n" +
			"Values are held in the platform keychain (macOS Keychain, the freedesktop\n" +
			"secret service on linux) and, where neither is available, in an encrypted\n" +
			"file vault. A value is never passed as a command-line argument, never\n" +
			"written to a log, and never exposed over MCP: `list` reports names only,\n" +
			"and `get` and `rotate` are elevated verbs that require local presence.",
		Annotations: map[string]string{"local": "true"},
	}
	cmd.AddCommand(
		newVaultSetCmd(deps),
		newVaultListCmd(deps),
		newVaultGetCmd(deps),
		newVaultRotateCmd(deps),
		newVaultImportCmd(deps),
		newVaultAuditCmd(deps),
	)
	return cmd
}

// vaultBroker builds the broker for one command invocation.
func vaultBroker(deps vaultDeps) (*secrets.Broker, error) {
	if deps.NewCustody == nil {
		return nil, cascade.New(cascade.KindInternal, "vault: no custody provider is configured")
	}
	custody, err := deps.NewCustody()
	if err != nil {
		return nil, err
	}
	return secrets.NewBroker(custody, deps.Gate)
}

// vaultOutputWriter mirrors status.go's statusOutputWriter, per this
// package's established per-file convention.
func vaultOutputWriter(cmd *cobra.Command) *output.Writer {
	jsonOut, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	verbose, _ := cmd.Flags().GetBool("verbose")
	noColor, _ := cmd.Flags().GetBool("no-color")
	return output.New(cmd.OutOrStdout(), cmd.OutOrStderr(), jsonOut, quiet, verbose, noColor)
}

// noInput reports whether CASCADE_NO_INPUT=1 forbids prompting.
func noInput(deps vaultDeps) bool {
	return deps.Getenv != nil && deps.Getenv("CASCADE_NO_INPUT") == "1"
}

// readSecretValue reads a value from --value-file, or from stdin when no
// file is named. It never reads a value from argv.
func readSecretValue(cmd *cobra.Command, deps vaultDeps, valueFile string) ([]byte, error) {
	if valueFile != "" {
		if deps.ReadFile == nil {
			return nil, cascade.New(cascade.KindInternal, "vault: no file reader is configured")
		}
		raw, err := deps.ReadFile(valueFile)
		if err != nil {
			return nil, cascade.Wrapf(cascade.KindNotFound, err, "vault: could not read the value file %q", valueFile)
		}
		return trimValue(raw), nil
	}
	raw, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxSecretValueBytes+1))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "vault: could not read the value from stdin")
	}
	if len(raw) > maxSecretValueBytes {
		return nil, cascade.Newf(cascade.KindInvalidInput,
			"vault: the value is larger than the %d-byte limit", maxSecretValueBytes)
	}
	return trimValue(raw), nil
}

// trimValue drops the single trailing newline a shell here-string or an
// echo adds, and nothing else: a value's interior bytes are never touched.
func trimValue(raw []byte) []byte {
	return []byte(strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r"))
}

// listView is `vault list`'s rendered result: names and the backend that
// holds them. There is no value field, by construction.
type listView struct {
	Names   []string `json:"names"`
	Backend string   `json:"backend"`
}

func newVaultListCmd(deps vaultDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List stored secret names (never their values)",
		Long: "List the names of every stored secret.\n\n" +
			"Values are never printed by this command and never travel over MCP; use\n" +
			"`cascade vault get NAME`, an elevated verb, to read one.",
		Example:     "  cascade vault list\n  cascade vault list --json",
		Args:        usageArgs(cobra.NoArgs),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			broker, err := vaultBroker(deps)
			if err != nil {
				return err
			}
			names, err := broker.List(cmd.Context())
			if err != nil {
				return err
			}
			if names == nil {
				names = []string{}
			}
			return vaultOutputWriter(cmd).Result(listView{Names: names, Backend: broker.Backend()})
		},
	}
}
