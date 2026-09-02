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
//
// R-14.128: each numeric core component is validated with the same
// leading-zero strictness as isValidSemver ("0" or "[1-9]\d*" only, never
// "01") — only the surrounding range OPERATORS are looser than full
// semver, not the numbers themselves. This is a byte-for-byte reuse of
// isValidSemver's per-component grammar, not a separate strictness
// decision. npm-style "||" alternation and "1.x"-style wildcard segments
// were never matched by this token grammar (a bare token can't contain "|"
// or a non-digit in a numeric position) and remain rejected; that has not
// changed here, only documented on Manifest.HostVersion.
var versionRangeTokenRe = regexp.MustCompile(
	`^(\^|~|>=|<=|>|<|=)?(0|[1-9]\d*)(\.(0|[1-9]\d*)){0,2}(-[0-9A-Za-z.-]+)?$`,
)

// isValidSemver reports whether s is a strict, full semver 2.0.0 string.
func isValidSemver(s string) bool {
	return semverRe.MatchString(s)
}

// idPatternRe matches the required plugin id shape stated on Manifest.ID's
// godoc and this ticket's own §WHAT: a lowercase ASCII letter followed by
// zero or more lowercase ASCII letters, digits, or hyphens. R-14.127 found
// this pattern documented but never enforced; enforcing it here closes an
// unmet acceptance criterion of the existing ticket, not new scope. If a
// later ticket relaxes the pattern, godoc and this regex must change
// together (R-14.127(c)).
var idPatternRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// reservedHostNamespace is the base of the "plugin.__host__.*" namespace
// R-14.100 reserves for host-owned storage slots (e.g.
// "plugin.__host__.metadata/<name>"). isReservedHostNamespace reports
// whether s equals that base or falls inside it.
const reservedHostNamespace = "plugin.__host__"

// isReservedHostNamespace reports whether s claims the plugin.__host__.*
// namespace reserved by R-14.100. Used by both the id check and the
// provides.domains[].Name check (R-14.127b).
func isReservedHostNamespace(s string) bool {
	return s == reservedHostNamespace || strings.HasPrefix(s, reservedHostNamespace+".")
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

// Validate checks m against all six rejection rules — plus the id-format
// (R-14.127) and reserved-namespace (R-14.127b) checks folded into R3 and
// R5 respectively, see ErrCodeRequiredField and ErrCodeCommandNameCollision
// — and returns every finding, ordered by the field's occurrence in the
// Manifest struct (id, schema, version, host_version, runtime,
// provides.domains, provides.commands, requires). It never short-circuits:
// a caller sees every problem in one pass. An empty (nil) return means m is
// valid.
func Validate(m Manifest) []ValidationError {
	var errs []ValidationError

	errs = append(errs, validateRequiredFields(m)...)
	errs = append(errs, validateReservedID(m)...)
	errs = append(errs, validateSchema(m)...)
	errs = append(errs, validateVersions(m)...)
	errs = append(errs, validateRuntime(m)...)
	errs = append(errs, validateReservedDomains(m)...)
	errs = append(errs, validateCommands(m)...)
	errs = append(errs, validateRequires(m)...)

	return errs
}

// validateRequiredFields implements rule R3: id and name must be non-empty,
// and id must match idPatternRe (R-14.127). The pattern check only runs
// when id is non-empty, so a missing id reports exactly one finding
// ("must not be empty"), not two.
func validateRequiredFields(m Manifest) []ValidationError {
	var errs []ValidationError
	if strings.TrimSpace(m.ID) == "" {
		errs = append(errs, ValidationError{Field: "id", Kind: ErrCodeRequiredField, Message: "id must not be empty"})
	} else if !idPatternRe.MatchString(m.ID) {
		errs = append(errs, ValidationError{
			Field: "id", Kind: ErrCodeRequiredField,
			Message: fmt.Sprintf("id %q must match [a-z][a-z0-9-]*", m.ID),
		})
	}
	if strings.TrimSpace(m.Name) == "" {
		errs = append(errs, ValidationError{Field: "name", Kind: ErrCodeRequiredField, Message: "name must not be empty"})
	}
	return errs
}

// validateReservedID implements rule R-14.127b for the id field: an id
// that claims the plugin.__host__.* namespace reserved by R-14.100 is
// refused. This is defence in depth, not redundant with idPatternRe — the
// reserved namespace contains "." and "_", which idPatternRe already
// excludes, so this check exists for the case a future, looser id pattern
// might otherwise let through.
func validateReservedID(m Manifest) []ValidationError {
	if isReservedHostNamespace(m.ID) {
		return []ValidationError{{
			Field: "id", Kind: ErrCodeCommandNameCollision,
			Message: fmt.Sprintf("id %q claims the plugin.__host__.* namespace reserved for host use (R-14.100)", m.ID),
		}}
	}
	return nil
}

// validateReservedDomains implements rule R-14.127b for
// provides.domains[].Name: a domain name that claims the
// plugin.__host__.* namespace reserved by R-14.100 is refused.
func validateReservedDomains(m Manifest) []ValidationError {
	var errs []ValidationError
	for i, d := range m.Provides.Domains {
		if isReservedHostNamespace(d.Name) {
			errs = append(errs, ValidationError{
				Field: fmt.Sprintf("provides.domains[%d].name", i),
				Kind:  ErrCodeCommandNameCollision,
				Message: fmt.Sprintf(
					"domain name %q claims the plugin.__host__.* namespace reserved for host use (R-14.100)",
					d.Name,
				),
			})
		}
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
