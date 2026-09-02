package plugin

import (
	"fmt"
	"regexp"
	"strings"
)

// Purpose: fail-closed validation of a decoded Manifest against the six
// normative rejection rules (R1-R6) from this ticket's contract.
// Inputs: a Manifest value (already TOML-decoded by loader.go).
// Outputs: the full, ordered slice of ValidationError findings — never a
//   short-circuited subset, so a plugin author sees every problem at once.
// Constraints: no bare fmt.Errorf/errors.New (boundary lint); pure
//   function, no I/O, no clock.
// SPORT: pkg/plugin manifest-v2-schema validate (ADD) — P1-E03-W1-S05-T6.

// reservedCommandNames is the core-noun + utility-verb blocklist a
// provides.commands entry's Name must never collide with (rule R5),
// transcribed verbatim from 07-CLI-COMMAND-TREE.md: the 14 core nouns
// (config daemon provider plugin node sync backup vault chat recall memory
// context fleet mcp) plus the 9 utility verbs (init run status doctor
// migrate self-update uninstall version completion). 23 entries total.
var reservedCommandNames = map[string]bool{
	// 14 core nouns.
	"config": true, "daemon": true, "provider": true, "plugin": true,
	"node": true, "sync": true, "backup": true, "vault": true,
	"chat": true, "recall": true, "memory": true, "context": true,
	"fleet": true, "mcp": true,
	// 9 utility verbs.
	"init": true, "run": true, "status": true, "doctor": true,
	"migrate": true, "self-update": true, "uninstall": true,
	"version": true, "completion": true,
}

// semverRe matches a strict, full semver 2.0.0 version string (the
// official semver.org grammar), used to validate the manifest's version
// field (rule R4).
var semverRe = regexp.MustCompile(
	`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
		`(-(0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(\.(0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*)?` +
		`(\+[0-9a-zA-Z-]+(\.[0-9a-zA-Z-]+)*)?$`,
)

// versionRangeTokenRe matches one constraint token within a host_version
// range: an optional comparator operator (^, ~, >=, <=, >, <, =) followed
// by a possibly-partial version core (major, major.minor, or
// major.minor.patch), with an optional pre-release suffix. 02-TARGET-
// STRUCTURE.md names "host version ranges" as a required field but does not
// specify the range grammar; this is the smallest sufficient shape chosen
// for this ticket (see DECISIONS in the ticket journal) — full strict
// semver is deliberately not required here because a range bound
// legitimately omits trailing components (e.g. "^1.2", ">=1").
var versionRangeTokenRe = regexp.MustCompile(
	`^(\^|~|>=|<=|>|<|=)?\d+(\.\d+){0,2}(-[0-9A-Za-z.-]+)?$`,
)

// isValidSemver reports whether s is a strict, full semver 2.0.0 string.
func isValidSemver(s string) bool {
	return semverRe.MatchString(s)
}

// isValidVersionRange reports whether s is a valid host_version range: the
// wildcard "*", or one or more whitespace-separated constraint tokens each
// matching versionRangeTokenRe.
func isValidVersionRange(s string) bool {
	if s == "*" {
		return true
	}
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return false
	}
	for _, tok := range fields {
		if !versionRangeTokenRe.MatchString(tok) {
			return false
		}
	}
	return true
}

// Validate checks m against all six rejection rules and returns every
// finding, ordered by the field's occurrence in the Manifest struct (id,
// name, schema, version, host_version, runtime, provides.commands,
// requires). It never short-circuits: a caller sees every problem in one
// pass. An empty (nil) return means m is valid.
func Validate(m Manifest) []ValidationError {
	var errs []ValidationError

	errs = append(errs, validateRequiredFields(m)...)
	errs = append(errs, validateSchema(m)...)
	errs = append(errs, validateVersions(m)...)
	errs = append(errs, validateRuntime(m)...)
	errs = append(errs, validateCommands(m)...)
	errs = append(errs, validateRequires(m)...)

	return errs
}

// validateRequiredFields implements rule R3: id and name must be non-empty.
func validateRequiredFields(m Manifest) []ValidationError {
	var errs []ValidationError
	if strings.TrimSpace(m.ID) == "" {
		errs = append(errs, ValidationError{Field: "id", Kind: ErrCodeRequiredField, Message: "id must not be empty"})
	}
	if strings.TrimSpace(m.Name) == "" {
		errs = append(errs, ValidationError{Field: "name", Kind: ErrCodeRequiredField, Message: "name must not be empty"})
	}
	return errs
}

// validateSchema implements rule R1: schema must equal SchemaVersion.
func validateSchema(m Manifest) []ValidationError {
	if m.Schema != SchemaVersion {
		return []ValidationError{{
			Field:   "schema",
			Kind:    ErrCodeSchemaVersion,
			Message: fmt.Sprintf("schema must equal %q, got %q", SchemaVersion, m.Schema),
		}}
	}
	return nil
}

// validateVersions implements rule R4: version must be a strict semver
// string, and host_version must be a valid version range.
func validateVersions(m Manifest) []ValidationError {
	var errs []ValidationError
	if !isValidSemver(m.Version) {
		errs = append(errs, ValidationError{
			Field: "version", Kind: ErrCodeMalformedVersion,
			Message: fmt.Sprintf("version %q is not a valid semver string", m.Version),
		})
	}
	if !isValidVersionRange(m.HostVersion) {
		errs = append(errs, ValidationError{
			Field: "host_version", Kind: ErrCodeMalformedVersion,
			Message: fmt.Sprintf("host_version %q is not a valid version range", m.HostVersion),
		})
	}
	return errs
}

// validateRuntime implements rule R2: runtime must be one of the four
// RuntimeMode values.
func validateRuntime(m Manifest) []ValidationError {
	if !m.Runtime.Valid() {
		return []ValidationError{{
			Field:   "runtime",
			Kind:    ErrCodeUnknownRuntimeMode,
			Message: fmt.Sprintf("runtime %q is not one of builtin|process|wasm|remote", m.Runtime),
		}}
	}
	return nil
}

// validateCommands implements rule R5: no provides.commands entry may
// collide with a reserved core noun or utility verb.
func validateCommands(m Manifest) []ValidationError {
	var errs []ValidationError
	for i, cmd := range m.Provides.Commands {
		name := strings.ToLower(strings.TrimSpace(cmd.Name))
		if reservedCommandNames[name] {
			errs = append(errs, ValidationError{
				Field: fmt.Sprintf("provides.commands[%d].name", i),
				Kind:  ErrCodeCommandNameCollision,
				Message: fmt.Sprintf(
					"command name %q collides with a reserved core noun or utility verb",
					cmd.Name,
				),
			})
		}
	}
	return errs
}

// validateRequires implements rule R6: every requires entry must be
// non-empty, and no entry may repeat.
func validateRequires(m Manifest) []ValidationError {
	var errs []ValidationError
	seen := make(map[string]bool, len(m.Requires))
	for i, entry := range m.Requires {
		trimmed := strings.TrimSpace(entry)
		field := fmt.Sprintf("requires[%d]", i)
		if trimmed == "" {
			errs = append(errs, ValidationError{
				Field: field, Kind: ErrCodeInvalidCapabilityRef,
				Message: "requires entry must not be empty",
			})
			continue
		}
		if seen[trimmed] {
			errs = append(errs, ValidationError{
				Field: field, Kind: ErrCodeInvalidCapabilityRef,
				Message: fmt.Sprintf("requires entry %q is a duplicate", trimmed),
			})
			continue
		}
		seen[trimmed] = true
	}
	return errs
}
