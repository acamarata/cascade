package config

// Purpose: the write pipeline of `cascade config set|unset|edit`
//   (P1-SEC-30): key and file secret screens, then the loosening
//   ceremony: the verb edits a staged copy, the previous and next
//   files' EffectiveConfig are compared, the file is written, and a
//   loosening calls Deps.AcceptLoosening with the written bytes' sha256.
// Inputs: the config path and the bytes before and after the verb's edit.
// Outputs: the written file; notices on stderr; an error only when the
//   write failed or the elevated call refused (no-input, elevation).
// Constraints: the compare is runtime.CompareSecurity over both files
//   loaded through runtime.Load with the same LoadOptions, and it always
//   runs (RequiresElevationToLoosen only picks the pre-flight notice). A
//   tightening never prompts. No daemon client, or an unreachable daemon,
//   prints looseningUnreachableMsg and exits 0: the daemon keeps the
//   restrictive values until the loosening is accepted.
// SPORT: cmd/cascade/config loosening ceremony (P1-SEC-30).

import (
	"errors"
	"io/fs"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// looseningUnreachableMsg tells the operator how a written loosening takes
// effect when the daemon could not accept it now.
const looseningUnreachableMsg = "config.toml written; its loosening applies only after `cascade config reload --accept-loosening` succeeds against a running daemon"

// readConfigBytes returns path's bytes, or nil when the file does not exist.
func readConfigBytes(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, cascade.Wrap(cascade.KindInternal, err, "read config.toml")
	}
	return data, nil
}

// stageFile writes data to a fresh owner-only scratch file.
func stageFile(data []byte) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", "cascade-config-stage-*.toml")
	if err != nil {
		return "", nil, cascade.Wrap(cascade.KindInternal, err, "create config stage file")
	}
	cleanup = func() { _ = os.Remove(f.Name()) }
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		cleanup()
		return "", nil, cascade.Wrap(cascade.KindInternal, werr, "write config stage file")
	}
	return f.Name(), cleanup, nil
}

// stagedEdit runs edit on a staged copy of path and returns the bytes
// before and after it; path itself is never touched here.
func stagedEdit(path string, edit func(w *runtime.ConfigWriter) error) (prev, next []byte, err error) {
	if prev, err = readConfigBytes(path); err != nil {
		return nil, nil, err
	}
	stage, cleanup, err := stageFile(prev)
	if err != nil {
		return nil, nil, err
	}
	defer cleanup()
	if err := edit(&runtime.ConfigWriter{Path: stage}); err != nil {
		return nil, nil, err
	}
	if next, err = os.ReadFile(stage); err != nil {
		return nil, nil, cascade.Wrap(cascade.KindInternal, err, "read config stage file")
	}
	return prev, next, nil
}

// commitConfigWrite compares prev and next, writes next to path and runs
// the loosening ceremony. Each of keys gets the pre-flight notice when its
// family needs elevation to loosen.
func commitConfigWrite(cmd *cobra.Command, deps Deps, path string, prev, next []byte, keys ...string) error {
	w := outputWriter(cmd)
	for _, k := range keys {
		if runtime.RequiresElevationToLoosen(k) {
			w.Warn("%s is in a guarded family: loosening it takes effect only through an elevated config.accept_loosening", k)
		}
	}
	loosened, err := looseningsBetween(cmd, deps, prev, next)
	if err != nil {
		return err
	}
	if err := runtime.WriteBytesAtomic(path, next); err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "write config.toml")
	}
	if len(loosened) == 0 {
		triggerReload(cmd, deps)
		return nil
	}
	return acceptWrittenLoosening(cmd, deps, next, loosened)
}

// looseningsBetween loads prev and next through runtime.Load with one set
// of LoadOptions and returns CompareSecurity's loosenings. A previous file
// that no longer loads is compared as the defaults, so the write is judged
// against the most restrictive baseline rather than skipped.
func looseningsBetween(cmd *cobra.Command, deps Deps, prev, next []byte) ([]runtime.LooseningPath, error) {
	opts := runtime.LoadOptions{Getenv: deps.Getenv, Environ: deps.Environ, Warn: func(string, ...interface{}) {}}
	before, err := loadStaged(cmd, opts, prev)
	if err != nil {
		before, err = loadStaged(cmd, opts, nil)
	}
	if err != nil {
		return nil, err
	}
	after, err := loadStaged(cmd, opts, next)
	if err != nil {
		if _, typed := cascade.KindOf(err); typed {
			return nil, err
		}
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "the new config.toml does not load; not written")
	}
	return runtime.CompareSecurity(runtime.EffectiveConfigOf(before), runtime.EffectiveConfigOf(after)), nil
}

// loadStaged runs runtime.Load over a staged copy of data.
func loadStaged(cmd *cobra.Command, opts runtime.LoadOptions, data []byte) (*runtime.Config, error) {
	stage, cleanup, err := stageFile(data)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	opts.Path = stage
	return runtime.Load(cmd.Context(), opts)
}

// acceptWrittenLoosening asks the daemon to accept the written bytes. Only
// an unreachable daemon (KindUnavailable) or no client is reported and
// exits 0; any other refusal is returned with its own kind.
func acceptWrittenLoosening(cmd *cobra.Command, deps Deps, written []byte, loosened []runtime.LooseningPath) error {
	w := outputWriter(cmd)
	keys := make([]string, len(loosened))
	for i, l := range loosened {
		keys[i] = l.Key
	}
	w.Warn("this change loosens %s", strings.Join(keys, ", "))
	if deps.AcceptLoosening == nil {
		w.Warn("%s", looseningUnreachableMsg)
		return nil
	}
	err := deps.AcceptLoosening(cmd.Context(), runtime.ConfigDigest(written))
	if err == nil {
		w.Warn("the daemon accepted the loosening")
		return nil
	}
	kind, typed := cascade.KindOf(err)
	if typed && kind == cascade.KindUnavailable {
		w.Warn("%s (%v)", looseningUnreachableMsg, err)
		return nil
	}
	if !typed {
		kind = cascade.KindElevationRequired
	}
	return cascade.Wrap(kind, err, "config.toml written but its loosening was not accepted; run `cascade config reload --accept-loosening`")
}

// screenKeyArg refuses a dotted key argument with a secret-shaped segment
// before any message (unknown key, suggestion, error prefix) can print it.
func screenKeyArg(key string) error {
	tree := map[string]interface{}{}
	node, segs := tree, strings.Split(key, ".")
	for _, seg := range segs[:len(segs)-1] {
		next := map[string]interface{}{}
		node[seg], node = next, next
	}
	node[segs[len(segs)-1]] = ""
	err := runtime.ScanTreeForSecrets(tree)
	if _, typed := cascade.KindOf(err); err == nil || typed {
		return err
	}
	return cascade.Wrap(cascade.KindInvalidInput, err, "config: the key argument")
}

// screenConfigBytes runs runtime.ScanTreeForSecrets over a candidate file.
func screenConfigBytes(data []byte) error {
	stage, cleanup, err := stageFile(data)
	if err != nil {
		return err
	}
	defer cleanup()
	tree, err := runtime.DecodeConfigFile(stage)
	if err != nil {
		return err
	}
	return runtime.ScanTreeForSecrets(tree)
}
