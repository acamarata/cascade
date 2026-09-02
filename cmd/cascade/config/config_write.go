package config

// Purpose: the write verbs this ticket owns outright — `set`, `unset`,
//   `edit` — over internal/runtime's ConfigWriter (config_writer.go)
//   and Validate.
// Inputs: cobra args (`set <key> <value>` or `set <key>=<value>`,
//   `unset <key>`) or $EDITOR content (`edit`).
// Outputs: the same output.Writer contract as config.go's read verbs; a
//   secret-shaped value or an invalid literal/key exits non-zero with an
//   actionable message and never touches disk.
// Constraints: same internal/output-only-printing rule as config.go.

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func newSetCmd(deps Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value> | <key>=<value>",
		Short: "Set one config key, preserving comments and structure",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value, err := splitSetArgs(args)
			if err != nil {
				return cascade.Wrap(cascade.KindInvalidInput, err, "config set")
			}
			w := &runtime.ConfigWriter{Path: deps.Paths.ConfigPath()}
			res, err := w.Set(key, value)
			if err != nil {
				return classifySetError(key, err)
			}
			triggerReload(cmd, deps)
			return outputWriter(cmd).Result(res)
		},
	}
}

// splitSetArgs accepts both 07-CLI-COMMAND-TREE.md's `set <key> <val>`
// shape and this ticket's own acceptance-criteria shape
// (`set retrieval.fusion.k=80`, one arg with an embedded `=`) — the two
// binding docs use different call shapes for the same verb, so this CLI
// supports both rather than picking one and silently breaking the other.
func splitSetArgs(args []string) (key, value string, err error) {
	if len(args) == 2 {
		return args[0], args[1], nil
	}
	key, value, ok := strings.Cut(args[0], "=")
	if !ok {
		return "", "", &runtime.DottedPathError{Path: args[0], Reason: "expected <key>=<value> or two separate arguments"}
	}
	return key, value, nil
}

// classifySetError maps a ConfigWriter.Set failure to the right taxonomy
// kind: a secret-shaped literal is invalid-input with an explicit
// vault-redirect message (already carried by *SecretLiteralError.Error());
// an unknown key or invalid literal is likewise invalid-input; anything
// else (a disk I/O failure) is internal.
func classifySetError(key string, err error) error {
	switch err.(type) {
	case *runtime.SecretLiteralError, *runtime.DottedPathError, *runtime.LiteralError, *runtime.ConfigError, *runtime.SchemaError:
		return cascade.Wrap(cascade.KindInvalidInput, err, "config set "+key)
	default:
		return cascade.Wrap(cascade.KindInternal, err, "config set "+key)
	}
}

func newUnsetCmd(deps Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "unset <key>",
		Short: "Remove one config key, reverting it to its schema default",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			w := &runtime.ConfigWriter{Path: deps.Paths.ConfigPath()}
			found, err := w.Unset(key)
			if err != nil {
				return classifySetError(key, err)
			}
			if found {
				triggerReload(cmd, deps)
			}
			return outputWriter(cmd).Result(unsetResult{Key: key, Removed: found})
		},
	}
}

// unsetResult is `cascade config unset`'s output shape: a small typed
// value with both a clean --json field set and a human-readable String(),
// instead of a bare map[string]interface{} (which output.Writer.Result
// would otherwise dump via Go's %v map syntax in human mode).
type unsetResult struct {
	Key     string `json:"key"`
	Removed bool   `json:"removed"`
}

// String implements fmt.Stringer.
func (r unsetResult) String() string {
	if r.Removed {
		return fmt.Sprintf("%s removed (now reverts to its schema default)", r.Key)
	}
	return fmt.Sprintf("%s was already unset (no change)", r.Key)
}

// editorCommand resolves $EDITOR, falling back to "vi" (the POSIX
// convention every terminal environment cascade targets ships).
func editorCommand(deps Deps) string {
	if e := deps.Getenv("EDITOR"); e != "" {
		return e
	}
	return "vi"
}

func newEditCmd(deps Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "edit",
		Short: "Open config.toml in $EDITOR, validating before it is applied",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := deps.Paths.ConfigPath()
			original, err := os.ReadFile(path)
			if err != nil && !os.IsNotExist(err) {
				return cascade.Wrap(cascade.KindInternal, err, "read config.toml")
			}
			tmp, err := os.CreateTemp("", "cascade-config-edit-*.toml")
			if err != nil {
				return cascade.Wrap(cascade.KindInternal, err, "create edit scratch file")
			}
			defer func() { _ = os.Remove(tmp.Name()) }()
			if _, err := tmp.Write(original); err != nil {
				_ = tmp.Close()
				return cascade.Wrap(cascade.KindInternal, err, "write edit scratch file")
			}
			if err := tmp.Close(); err != nil {
				return cascade.Wrap(cascade.KindInternal, err, "close edit scratch file")
			}

			editorCmd := exec.CommandContext(cmd.Context(), editorCommand(deps), tmp.Name()) //nolint:gosec // $EDITOR is an intentional, user-controlled launch, the standard `git commit`/`crontab -e` pattern
			editorCmd.Stdin = cmd.InOrStdin()
			editorCmd.Stdout = cmd.OutOrStdout()
			editorCmd.Stderr = cmd.OutOrStderr()
			if err := editorCmd.Run(); err != nil {
				return cascade.Wrap(cascade.KindUnavailable, err, "run $EDITOR")
			}

			edited, err := os.ReadFile(tmp.Name())
			if err != nil {
				return cascade.Wrap(cascade.KindInternal, err, "read edited config")
			}
			tree, err := runtime.DecodeConfigFile(tmp.Name())
			if err != nil {
				return cascade.Wrap(cascade.KindInvalidInput, err, "edited config.toml is invalid; not applied")
			}
			if err := runtime.Validate(tree); err != nil {
				return cascade.Wrap(cascade.KindInvalidInput, err, "edited config.toml failed validation; not applied")
			}
			if err := writeConfigFile(path, edited); err != nil {
				return cascade.Wrap(cascade.KindInternal, err, "apply edited config.toml")
			}
			triggerReload(cmd, deps)
			return outputWriter(cmd).Result("config.toml updated and validated")
		},
	}
}

// writeConfigFile is the edit verb's own crash-safe write — identical
// temp-in-same-dir + rename shape as internal/runtime's writeBytesAtomic,
// duplicated here only because that helper is unexported across the
// package boundary; both implement the same R-14.106 pattern.
func writeConfigFile(path string, data []byte) error {
	dir := path[:strings.LastIndexByte(path, '/')+1]
	if dir == "" {
		dir = "."
	}
	tmp, err := os.CreateTemp(dir, ".config-*.toml.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// triggerReload best-effort-notifies a running daemon after a successful
// write (08 §3: "on success, triggers hot-reload path — in daemon mode:
// SIGHUP; daemonless: re-read in-process via HotReloader.Reload()"). A
// one-shot CLI process has no persistent HotReloader of its own to
// re-read into (there is no long-lived state in this process to update),
// so the daemonless case here is a documented no-op: the file is already
// updated on disk, which is the only state a daemonless invocation of
// cascade ever had in the first place. When a daemon IS running, the
// same SIGHUP-via-pidfile path `cascade config reload` uses is invoked;
// failure is intentionally non-fatal (a Set/Unset/Edit that already
// wrote a valid file to disk must not itself fail just because no
// daemon happened to be listening).
func triggerReload(_ *cobra.Command, deps Deps) {
	_ = sendReloadSignal(deps) // best-effort; see doc comment above
}
