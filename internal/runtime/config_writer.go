package runtime

// Purpose: ConfigWriter — the write-then-validate-then-persist
//
//	orchestrator behind `cascade config set`/`unset`, tying together the
//	dotted-path resolver (config_write.go), the secret detector
//	(config_write_secrets.go), the structure-preserving line editor
//	(toml_edit.go/toml_edit_scanner.go), Validate, and the atomic write.
//	Split out of toml_edit.go per R-14.117/Art.10.3 (300-line file cap):
//	behaviour-preserving relocation only, no logic change.
//
// Inputs: a dotted key and a literal-value string (Set), or a dotted key
//
//	alone (Unset).
//
// Outputs: a *WriteSetResult on success, or a typed error with disk left
//
//	exactly as it was on any failure.
//
// Constraints: same as toml_edit.go — never round-trips through
//
//	toml.Marshal (would destroy comments); Validate always runs before
//	any write.
//
// SPORT: runtime/toml-edit-engine (ADD, placeholder per T-8 sport_updates).

import (
	"fmt"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// ConfigWriter is the write-then-validate-then-persist orchestrator behind
// `cascade config set`/`unset`: resolve the dotted key, apply the
// structure-preserving edit in memory, decode the result and run
// Validate, and only then call the existing atomic write helper. Disk is
// never touched when any step fails.
type ConfigWriter struct {
	// Path is the config.toml location to edit.
	Path string
}

// WriteSetResult carries the parsed value actually written, for callers
// (the CLI `set` verb) that want to echo it back or feed it to the
// hot-reload path without re-reading the file.
type WriteSetResult struct {
	Value    interface{}
	Existed  bool
	KeyPath  string
	RawInput string
}

// String implements fmt.Stringer so internal/output.Writer.Result renders
// a clean one-line human-mode message ("key = value") instead of Go's
// default %v struct dump.
func (r *WriteSetResult) String() string {
	return fmt.Sprintf("%s = %v", r.KeyPath, r.Value)
}

// Set validates literalRaw as a TOML literal, refuses a secret-shaped
// value, resolves dotted against the known-key registry, applies the
// structure-preserving edit, decodes and Validates the result, and
// (only on success) writes it atomically to w.Path. On any failure disk
// is left exactly as it was.
func (w *ConfigWriter) Set(dotted, literalRaw string) (*WriteSetResult, error) {
	value, err := ParseTomlLiteral(literalRaw)
	if err != nil {
		return nil, err
	}
	if err := checkLiteralForSecrets(dotted, value); err != nil {
		return nil, err
	}
	if _, err := ResolveDottedPath(dotted); err != nil {
		return nil, err
	}

	src, err := readOptionalFile(w.Path)
	if err != nil {
		return nil, err
	}
	edited, err := SetKeyLine(src, dotted, strings.TrimSpace(literalRaw))
	if err != nil {
		return nil, err
	}
	tree, err := decodeForValidate(edited)
	if err != nil {
		return nil, err
	}
	if err := Validate(tree); err != nil {
		return nil, err
	}
	if err := writeBytesAtomic(w.Path, edited); err != nil {
		return nil, err
	}
	return &WriteSetResult{Value: value, KeyPath: dotted, RawInput: literalRaw}, nil
}

// Unset resolves dotted, removes its line (if present), decodes and
// Validates the result, and (only on success) writes it atomically. An
// absent key is a successful no-op (matching UnsetKeyLine) that still
// writes nothing, since the file did not change.
func (w *ConfigWriter) Unset(dotted string) (bool, error) {
	if _, err := ResolveDottedPath(dotted); err != nil {
		return false, err
	}
	src, err := readOptionalFile(w.Path)
	if err != nil {
		return false, err
	}
	edited, found, err := UnsetKeyLine(src, dotted)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	tree, err := decodeForValidate(edited)
	if err != nil {
		return false, err
	}
	if err := Validate(tree); err != nil {
		return false, err
	}
	if err := writeBytesAtomic(w.Path, edited); err != nil {
		return false, err
	}
	return true, nil
}

// decodeForValidate decodes edited as TOML into a generic tree for
// Validate to check; a decode failure means the edit produced malformed
// TOML (a bug in this editor, or an adversarial literal that slipped past
// ParseTomlLiteral) and is reported as a *ConfigError rather than a raw
// go-toml error, matching every other parse failure in this package.
func decodeForValidate(edited []byte) (map[string]interface{}, error) {
	tree := map[string]interface{}{}
	if err := toml.Unmarshal(edited, &tree); err != nil {
		return nil, &ConfigError{Reason: fmt.Sprintf("edited config would be malformed TOML: %v", err)}
	}
	return tree, nil
}
