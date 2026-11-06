package learn

// Purpose: RuntimeConfigReader, the real raw reader of one config.toml key:
//   its literal as written in the file, whether the file sets it, the
//   effective source runtime.Load reports for it, and the file's digest.
// Inputs: a config.toml path and a concrete dotted key.
// Outputs: the raw TOML literal, presence, the effective ConfigSource and
//   runtime.ConfigDigest of the file bytes ("" when the file is missing).
// Constraints: the key is checked by TargetForConfigKey before any read. A
//   missing file is absence, never an error; every other read or parse
//   failure is an error, never absence. The key is found by walking its
//   split dotted path through the decoded document.
// SPORT: internal.learn.RuntimeConfigReader/ADDED (P1-LRN-03).

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ConfigValueReader reads one dotted config key. raw is the TOML literal
// the file holds, present reports whether the file sets the key, source is
// the precedence level of the effective value and fileDigest is the digest
// of the bytes read. A read or parse failure is an error, never absence.
type ConfigValueReader interface {
	Get(dotted string) (raw string, present bool, source runtime.ConfigSource, fileDigest string, err error)
}

// RuntimeConfigReader reads config.toml at Path for real: the raw literal
// from the file itself, the effective source from runtime.Load, and the
// digest from runtime.ConfigDigest. A missing file reads as absent with an
// empty digest; malformed TOML, a table where a value was expected or an
// unsupported literal is KindInvalidInput; any other read failure is
// KindUnavailable.
type RuntimeConfigReader struct {
	Path string
}

// Get reads dotted from r.Path. See RuntimeConfigReader.
func (r RuntimeConfigReader) Get(dotted string) (string, bool, runtime.ConfigSource, string, error) {
	if r.Path == "" {
		return "", false, runtime.SourceDefault, "", cascade.New(cascade.KindInvalidInput, "learn: config reader needs a file path")
	}
	if _, _, err := TargetForConfigKey(dotted); err != nil {
		return "", false, runtime.SourceDefault, "", err
	}
	data, err := os.ReadFile(r.Path)
	if errors.Is(err, fs.ErrNotExist) {
		source, lerr := effectiveSource(r.Path, dotted)
		return "", false, source, "", lerr
	}
	if err != nil {
		return "", false, runtime.SourceDefault, "", cascade.Wrap(cascade.KindUnavailable, err, "learn: config file is unreadable")
	}
	digest := runtime.ConfigDigest(data)
	raw, present, err := rawLiteral(data, dotted)
	if err != nil {
		return "", false, runtime.SourceDefault, digest, err
	}
	source, err := effectiveSource(r.Path, dotted)
	if err != nil {
		return "", false, runtime.SourceDefault, digest, err
	}
	return raw, present, source, digest, nil
}

// effectiveSource is runtime.Load's source for dotted. A Load error keeps
// its kind, or becomes KindInvalidInput when it carries none.
func effectiveSource(path, dotted string) (runtime.ConfigSource, error) {
	cfg, err := runtime.Load(context.Background(), runtime.LoadOptions{Path: path})
	if err != nil {
		if _, ok := cascade.KindOf(err); ok {
			return runtime.SourceDefault, err
		}
		return runtime.SourceDefault, cascade.Wrap(cascade.KindInvalidInput, err, "learn: config file does not load")
	}
	return cfg.Source(dotted), nil
}

// rawLiteral decodes data and walks dotted's segments to its leaf.
func rawLiteral(data []byte, dotted string) (string, bool, error) {
	var tree map[string]any
	if err := toml.Unmarshal(data, &tree); err != nil {
		return "", false, cascade.Wrap(cascade.KindInvalidInput, err, "learn: config file is malformed TOML")
	}
	var node any = tree
	for _, seg := range strings.Split(dotted, ".") {
		table, ok := node.(map[string]any)
		if !ok {
			return "", false, cascade.New(cascade.KindInvalidInput, "learn: config key passes through a non-table value")
		}
		if node, ok = table[seg]; !ok {
			return "", false, nil
		}
	}
	if _, isTable := node.(map[string]any); isTable {
		return "", false, cascade.New(cascade.KindInvalidInput, "learn: config key names a table, not a value")
	}
	raw, err := formatLiteral(node, true)
	if err != nil {
		return "", false, err
	}
	return raw, true, nil
}

// formatLiteral renders a decoded scalar, or an array of scalars when
// allowArray is set, as a TOML literal.
func formatLiteral(v any, allowArray bool) (string, error) {
	switch x := v.(type) {
	case string:
		return strconv.Quote(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), nil
	case bool:
		return strconv.FormatBool(x), nil
	case []any:
		if !allowArray {
			break
		}
		parts := make([]string, len(x))
		for i, e := range x {
			s, err := formatLiteral(e, false)
			if err != nil {
				return "", err
			}
			parts[i] = s
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	}
	return "", cascade.New(cascade.KindInvalidInput, "learn: config value is not a supported TOML literal")
}
