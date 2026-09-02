package config

// Purpose: `cascade config` — the CLI surface over internal/runtime's
//   config load/write/hot-reload engine (07-CLI-COMMAND-TREE.md §config,
//   08-INIT-CONFIG-SPEC.md §3): get/set/unset/list/validate/edit/reload/
//   path. Per the ticket contract's ownership split, this file MOUNTS
//   the read-verb behavior C/S-04.T1 already scaffolded in
//   internal/runtime (ListEffectiveHandler for `list --effective`,
//   PathHandler for `path`) and implements `get`/`validate` as thin
//   glue directly over that same already-public API (*Config.
//   EffectiveEntries/Source, runtime.Validate) — no new internal/runtime
//   read capability is added here, only CLI wiring. The WRITE verbs
//   (set/unset/edit/reload) are this ticket's own new implementation,
//   split into config_write.go and config_reload*.go.
// Inputs: cobra args/flags; the resolved config.toml path (via an
//   injected PathProvider) and process environment.
// Outputs: process output via internal/output.Writer (human text or the
//   --json envelope); a non-zero taxonomy exit code on failure.
// Constraints: NEVER prints via os.Stdout/fmt.Print directly (D/S-06.T5,
//   forbidigo-enforced in cmd/**) — every command builds an
//   internal/output.Writer over cmd.OutOrStdout()/cmd.OutOrStderr(),
//   which defaults to the real streams in production and is
//   test-overridable via cobra's SetOut/SetErr, exactly like every other
//   cmd/cascade/* command. MOUNTING NOTE: cmd/cascade/root.go (D/S-06.T1)
//   is explicitly out of this ticket's files_scope and has no extension
//   registry yet for a later package to hook into without editing it —
//   NewConfigCmd is exported and ready to be added via a one-line
//   `root.AddCommand(config.NewConfigCmd(...))` in root.go the moment
//   that ticket (or a follow-up) adds the call; see this ticket's final
//   report for the precise blocker.
// SPORT: cmd/cascade/config (ADD, placeholder per T-8 sport_updates).

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Deps carries every external input the config command tree needs,
// injected once at construction so tests never touch the real process
// environment or home directory (Art.7.1).
type Deps struct {
	Paths  runtime.PathProvider
	Getenv runtime.Getenv
	Clock  runtime.Clock
	// Environ matches os.Environ's signature.
	Environ func() []string
}

// NewConfigCmd builds the `config` command tree. deps must be fully
// populated by the caller (production: Bootstrap's PathProvider/
// SystemClock/os.Environ; tests: fakes rooted at t.TempDir()).
func NewConfigCmd(deps Deps) *cobra.Command {
	root := &cobra.Command{
		Use:   "config",
		Short: "Inspect and edit cascade's config.toml",
	}
	root.AddCommand(newGetCmd(deps))
	root.AddCommand(newSetCmd(deps))
	root.AddCommand(newUnsetCmd(deps))
	root.AddCommand(newListCmd(deps))
	root.AddCommand(newValidateCmd(deps))
	root.AddCommand(newEditCmd(deps))
	root.AddCommand(newReloadCmd(deps))
	root.AddCommand(newPathCmd(deps))
	return root
}

// outputWriter builds an internal/output.Writer bound to cmd's own
// out/err streams (real stdout/stderr in production, test buffers under
// cobra's SetOut/SetErr) and the standard --json/-q/-v/--no-color flags,
// tolerating any of them being unregistered (defaults to false) so this
// command tree stays testable standalone, before it is mounted under the
// real root.
func outputWriter(cmd *cobra.Command) *output.Writer {
	jsonOut, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	verbose, _ := cmd.Flags().GetBool("verbose")
	noColor, _ := cmd.Flags().GetBool("no-color")
	return output.New(cmd.OutOrStdout(), cmd.OutOrStderr(), jsonOut, quiet, verbose, noColor)
}

// loadConfig loads the current config.toml through the exact same Load
// entry point the daemon and every other CLI command use (single
// resolution model, 08 §2).
func loadConfig(ctx context.Context, deps Deps) (*runtime.Config, error) {
	opts := runtime.LoadOptions{
		Path:    deps.Paths.ConfigPath(),
		Getenv:  deps.Getenv,
		Environ: deps.Environ,
	}
	cfg, err := runtime.Load(ctx, opts)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "load config.toml")
	}
	return cfg, nil
}

func newGetCmd(deps Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Print one config key's resolved (effective) value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd.Context(), deps)
			if err != nil {
				return err
			}
			key := args[0]
			w := outputWriter(cmd)
			for _, e := range cfg.EffectiveEntries() {
				if e.Key == key {
					if w.Mode().JSON {
						return w.Result(e)
					}
					return w.Result(getResultString{e})
				}
			}
			return cascade.Wrap(cascade.KindNotFound, &runtime.DottedPathError{Path: key, Reason: "unknown config key"}, "config get")
		},
	}
}

func newListCmd(deps Deps) *cobra.Command {
	var effective bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List every config key (--effective: merged view with per-key source)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(cmd.Context(), deps)
			if err != nil {
				return err
			}
			w := outputWriter(cmd)
			if w.Mode().JSON {
				entries := cfg.EffectiveEntries()
				return w.Result(entries)
			}
			text, err := runtime.ListEffectiveHandler(cfg, false)
			if err != nil {
				return cascade.Wrap(cascade.KindInternal, err, "render config list")
			}
			w.Println(text)
			return nil
		},
	}
	cmd.Flags().BoolVar(&effective, "effective", true, "show the merged effective view (default; the only view this ticket implements)")
	return cmd
}

func newValidateCmd(deps Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate config.toml without applying it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tree, err := readConfigTree(deps)
			if err != nil {
				return err
			}
			if err := runtime.Validate(tree); err != nil {
				return cascade.Wrap(cascade.KindInvalidInput, err, "config.toml is invalid")
			}
			return outputWriter(cmd).Result("config.toml is valid")
		},
	}
}

func newPathCmd(deps Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print resolved filesystem paths",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := outputWriter(cmd)
			text, err := runtime.PathHandler(deps.Paths, w.Mode().JSON)
			if err != nil {
				return cascade.Wrap(cascade.KindInternal, err, "render config path")
			}
			w.Println(text)
			return nil
		},
	}
}

// readConfigTree reads and decodes config.toml's raw tree (tolerating a
// not-yet-existing file as empty), for the handlers that need to call
// runtime.Validate directly rather than through a resolved *Config
// (validate must reject a malformed file that Load's own TOML decode
// would otherwise choke on before Validate ever ran; readRawTree
// surfaces that decode error with the same taxonomy kind as any other
// validation failure).
func readConfigTree(deps Deps) (map[string]interface{}, error) {
	tree, err := runtime.DecodeConfigFile(deps.Paths.ConfigPath())
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "config.toml is invalid")
	}
	return tree, nil
}

// getResultString renders a runtime.EffectiveEntry as "key = value
// (source)" for `cascade config get`'s human-mode output, instead of
// output.Writer.Result's %v struct-dump fallback.
type getResultString struct {
	entry runtime.EffectiveEntry
}

// String implements fmt.Stringer.
func (g getResultString) String() string {
	return fmt.Sprintf("%s = %v (%s)", g.entry.Key, g.entry.Value, g.entry.Source)
}
