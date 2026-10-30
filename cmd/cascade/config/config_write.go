package config

// Purpose: the write verbs this ticket owns outright — `set`, `unset`,
//   `edit` — over internal/runtime's ConfigWriter (config_writer.go)
//   and Validate.
// Inputs: cobra args (`set <key> <value>` or `set <key>=<value>`,
//   `unset <key>`) or $EDITOR content (`edit`).
// Outputs: the same output.Writer contract as config.go's read verbs; a
//   secret-shaped value or an invalid literal/key exits non-zero with an
//   actionable message and never touches disk.
// Constraints: same internal/output-only-printing rule as config.go. Every
//   write goes through commitConfigWrite (config_loosening.go); set and edit
//   screen the whole candidate file with runtime.ScanTreeForSecrets, whose
//   scanner is ProcessSecretLiteralScanner in the cascade binary.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// minOpaqueRun is the shortest unnamed entropy run ProcessSecretLiteralScanner
// counts: two 16-character halves (the detector's minimum run) joined.
const minOpaqueRun = 32

// processDetector is secrets.ProcessDetector; a test replaces it to prove a
// detector failure refuses.
var processDetector = secrets.ProcessDetector

// ProcessSecretLiteralScanner is the runtime.SecretLiteralScanner cascade
// installs at init: the pattern and entropy passes of
// secrets.ProcessDetector over one value. A hit counts when it reaches the
// detector's confidence threshold, or when it is an unnamed entropy run of
// at least minOpaqueRun characters with no '/' or '.' (path and host text).
// A detector that cannot be resolved reports a hit, so the write refuses.
func ProcessSecretLiteralScanner(value string) []string {
	d, err := processDetector()
	if err != nil {
		return []string{"detector_unavailable"}
	}
	threshold := secrets.Confidence(d.Config().ConfidenceThreshold)
	var classes []string
	for _, h := range d.Scan([]byte(value)) {
		span := strings.TrimRight(value[h.Offset:h.Offset+h.Len], "=")
		opaque := h.Pattern == "entropy" && h.Confidence == secrets.ConfidenceWeak &&
			len(span) >= minOpaqueRun && !strings.ContainsAny(span, "/.")
		if h.Confidence >= threshold || opaque {
			classes = append(classes, string(h.Class))
		}
	}
	return classes
}

func newSetCmd(deps Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value> | <key>=<value>",
		Short: "Set one config key, preserving comments and structure",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value, err := splitSetArgs(args)
			if err == nil {
				err = screenKeyArg(key)
			}
			if err != nil {
				if _, typed := cascade.KindOf(err); typed {
					return err
				}
				return cascade.Wrap(cascade.KindInvalidInput, err, "config set")
			}
			path := deps.Paths.ConfigPath()
			if err := checkConfigPerm(cmd, path); err != nil {
				return err // an unsafe or uninspectable config is never written
			}
			var res *runtime.WriteSetResult
			prev, next, err := stagedEdit(path, func(w *runtime.ConfigWriter) (err error) {
				res, err = w.Set(key, value)
				return err
			})
			if err == nil {
				err = screenConfigBytes(next)
			}
			if err != nil {
				return classifySetError(key, err)
			}
			if err := commitConfigWrite(cmd, deps, path, prev, next, key); err != nil {
				return err
			}
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

// classifySetError maps a write failure to its taxonomy kind: a
// secret-shaped value (every hit, with the vault redirect), an unknown key
// or an invalid literal is invalid-input; an already-typed error keeps its
// kind (no installed scanner stays unavailable); anything else is internal.
func classifySetError(key string, err error) error {
	var secret *runtime.SecretLiteralError
	if errors.As(err, &secret) {
		return cascade.Wrap(cascade.KindInvalidInput, err, "config set "+key)
	}
	switch err.(type) {
	case *runtime.DottedPathError, *runtime.LiteralError, *runtime.ConfigError, *runtime.SchemaError:
		return cascade.Wrap(cascade.KindInvalidInput, err, "config set "+key)
	}
	if _, typed := cascade.KindOf(err); typed {
		return err
	}
	return cascade.Wrap(cascade.KindInternal, err, "config set "+key)
}

func newUnsetCmd(deps Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "unset <key>",
		Short: "Remove one config key, reverting it to its schema default",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, path := args[0], deps.Paths.ConfigPath()
			if err := screenKeyArg(key); err != nil {
				return err
			}
			if err := checkConfigPerm(cmd, path); err != nil {
				return err // an unsafe or uninspectable config is never written
			}
			var found bool
			prev, next, err := stagedEdit(path, func(w *runtime.ConfigWriter) (err error) {
				found, err = w.Unset(key)
				return err
			})
			if err != nil {
				return classifySetError(key, err)
			}
			if found {
				if err := commitConfigWrite(cmd, deps, path, prev, next, key); err != nil {
					return err
				}
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
			if err := checkConfigPerm(cmd, path); err != nil {
				return err // an unsafe or uninspectable config is never written
			}
			prev, err := readConfigBytes(path)
			if err != nil {
				return err
			}
			tmpPath, cleanup, err := stageFile(prev)
			if err != nil {
				return err
			}
			defer cleanup()

			if err := runEditor(cmd, deps, tmpPath); err != nil {
				return err
			}

			edited, _, err := readAndCheckEditedConfig(tmpPath)
			if err != nil {
				return err
			}
			if err := commitConfigWrite(cmd, deps, path, prev, edited); err != nil {
				return err
			}
			return outputWriter(cmd).Result("config.toml updated and validated")
		},
	}
}

// runEditor launches deps' $EDITOR (or vi) against tmpPath, wired to
// cmd's own stdio so an interactive editor works exactly as it would from
// a real terminal.
func runEditor(cmd *cobra.Command, deps Deps, tmpPath string) error {
	editorCmd := exec.CommandContext(cmd.Context(), editorCommand(deps), tmpPath) //nolint:gosec // $EDITOR is an intentional, user-controlled launch, the standard `git commit`/`crontab -e` pattern
	editorCmd.Stdin = cmd.InOrStdin()
	editorCmd.Stdout = cmd.OutOrStdout()
	editorCmd.Stderr = cmd.OutOrStderr()
	if err := editorCmd.Run(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "run $EDITOR")
	}
	return nil
}

// readAndCheckEditedConfig reads tmpPath back, decodes it, and runs the
// same two gates a write must always pass before it reaches disk:
// Validate (shape) and ScanTreeForSecrets (R-14 CR fix, P1-E03-W1-S05-T8,
// blocking fix 3 — `edit` previously applied neither the secret screen
// nor anything beyond Validate, so a secret pasted through $EDITOR
// reached disk in plaintext, the exact value class `set` already
// refuses). Disk is untouched by this function; the caller only writes
// edited once both gates pass.
func readAndCheckEditedConfig(tmpPath string) (edited []byte, tree map[string]interface{}, err error) {
	edited, err = os.ReadFile(tmpPath)
	if err != nil {
		return nil, nil, cascade.Wrap(cascade.KindInternal, err, "read edited config")
	}
	tree, err = runtime.DecodeConfigFile(tmpPath)
	if err != nil {
		return nil, nil, cascade.Wrap(cascade.KindInvalidInput, err, "edited config.toml is invalid; not applied")
	}
	if err := runtime.Validate(tree); err != nil {
		return nil, nil, cascade.Wrap(cascade.KindInvalidInput, err, "edited config.toml failed validation; not applied")
	}
	if err := runtime.ScanTreeForSecrets(tree); err != nil {
		if _, typed := cascade.KindOf(err); typed {
			return nil, nil, err // no installed scanner: unavailable, not applied
		}
		return nil, nil, cascade.Wrap(cascade.KindInvalidInput, err, "edited config.toml not applied")
	}
	return edited, tree, nil
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
