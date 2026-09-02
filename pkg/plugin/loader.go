package plugin

import (
	"io"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: parse a cascade.plugin/v2 manifest document into a Manifest.
// Inputs: an io.Reader over a TOML document.
// Outputs: a fully validated Manifest, or a *cascade.Error describing why
//   parsing/validation failed.
// Constraints: TOML-only per T0 ruling R-14.10 (no format parameter, no
//   YAML decoder dependency); strict-mode unknown-key rejection; fail-closed
//   — a caller never receives a non-zero Manifest alongside a non-nil error;
//   no bare fmt.Errorf/errors.New (boundary lint).
// SPORT: pkg/plugin manifest-v2-schema loader (ADD) — P1-E03-W1-S05-T6.

// ParseManifest decodes a cascade.plugin/v2 TOML document from r into a
// Manifest and validates it against all six rejection rules (see Validate
// in validate.go). The manifest format is TOML-only (R-14.10): there is no
// format parameter and no second wire-format decoder.
//
// Decoding is strict: any top-level or nested key present in the document
// that does not correspond to a field on Manifest (or one of its component
// types) is rejected with ErrCodeParse, exactly like a syntactically
// malformed document — BurntSushi/toml has no DisallowUnknownFields
// decoder option, so strictness is achieved by decoding then inspecting the
// returned MetaData.Undecoded() key list, which is empty only when every
// key in the document was consumed by a Manifest field.
//
// ParseManifest is fail-closed: on any decode error or any validation
// failure, it returns the zero Manifest and a non-nil error — never a
// partially populated Manifest alongside an error. When Validate reports
// more than one ValidationError, ParseManifest wraps and returns the first
// (in field-occurrence order); callers that need the full set call Validate
// directly on a successfully decoded Manifest.
func ParseManifest(r io.Reader) (Manifest, error) {
	var m Manifest

	meta, err := toml.NewDecoder(r).Decode(&m)
	if err != nil {
		return Manifest{}, cascade.Wrap(cascade.KindInvalidInput, err, "plugin manifest: decode TOML")
	}

	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return Manifest{}, cascade.Newf(
			ErrCodeParse.Kind(),
			"plugin manifest: unknown key(s): %s", strings.Join(keys, ", "),
		)
	}

	if errs := Validate(m); len(errs) > 0 {
		first := errs[0]
		return Manifest{}, cascade.Wrap(first.Kind.Kind(), first, "plugin manifest: validation failed")
	}

	return m, nil
}
