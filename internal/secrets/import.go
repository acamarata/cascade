// Purpose: the vault.env import shim - parse a KEY=value environment file
//
//	and bulk-load its entries into the broker.
//
// Inputs: vault.env-format bytes (KEY=value lines, # comments, blanks).
// Outputs: the parsed entries in file order, and an ImportReport counting
//
//	what landed.
//
// Constraints: the parser is a fail-closed parser, not a best-effort one.
//
//	A line it cannot read is a refusal naming the LINE NUMBER - never the
//	line's content, because the content is a secret value. Import is
//	idempotent: a second run of the same file overwrites in place and
//	reports success. Nothing here logs, prints, or writes a value
//	anywhere but into the broker.
//
// SPORT: internal/secrets VaultEnvImport/ADDED.

package secrets

import (
	"bufio"
	"bytes"
	"context"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// maxEnvLineLen bounds a single vault.env line. A file with a line longer
// than this is refused rather than streamed into the vault: it is far more
// likely to be a binary file handed to the wrong command than a secret.
const maxEnvLineLen = 1 << 20

// EnvEntry is one parsed KEY=value pair.
type EnvEntry struct {
	// Name is the validated secret name.
	Name string
	// Value is the raw secret bytes. It is never rendered, logged, or
	// included in an error.
	Value []byte
	// Line is the 1-based source line, used for diagnostics that must
	// locate a problem without quoting it.
	Line int
}

// ParseVaultEnv reads vault.env-format bytes into entries, in file order.
//
// Accepted: blank lines, whole-line and trailing-free `#` comments,
// `KEY=value`, `export KEY=value`, and a value wrapped in matching single
// or double quotes. A double-quoted value has \n, \r, \t, \\ and \"
// unescaped; a single-quoted value is taken literally, as a POSIX shell
// would.
//
// Refused (each with the line number and no content): a line with no '=',
// an invalid name, an unterminated quote, an embedded NUL, and any line
// over the length cap. A duplicate key is NOT an error: later wins, which
// is what makes a re-import idempotent, and the report counts it.
func ParseVaultEnv(data []byte) ([]EnvEntry, error) {
	var out []EnvEntry
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), maxEnvLineLen)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		entry, ok, err := parseEnvLine(scanner.Text(), lineNo)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, entry)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, err,
			"secrets: vault.env line %d could not be read (a line may exceed the %d-byte limit)", lineNo+1, maxEnvLineLen)
	}
	return out, nil
}

// parseEnvLine parses one line. ok is false for a line that carries no
// entry (blank or comment).
func parseEnvLine(line string, lineNo int) (EnvEntry, bool, error) {
	if strings.ContainsRune(line, 0) {
		return EnvEntry{}, false, errEnvLine(lineNo, "contains a NUL byte")
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return EnvEntry{}, false, nil
	}
	trimmed = strings.TrimPrefix(trimmed, "export ")
	eq := strings.Index(trimmed, "=")
	if eq < 0 {
		return EnvEntry{}, false, errEnvLine(lineNo, "is not a KEY=value assignment")
	}
	name := strings.TrimSpace(trimmed[:eq])
	if err := validateSecretName(name); err != nil {
		return EnvEntry{}, false, cascade.Wrapf(cascade.KindInvalidInput, err,
			"secrets: vault.env line %d has an unusable key", lineNo)
	}
	value, err := parseEnvValue(strings.TrimSpace(trimmed[eq+1:]), lineNo)
	if err != nil {
		return EnvEntry{}, false, err
	}
	return EnvEntry{Name: name, Value: []byte(value), Line: lineNo}, true, nil
}

// parseEnvValue unwraps quoting. An opening quote with no matching close is
// refused rather than silently treated as literal text: guessing there
// stores a value with a stray quote in it, which fails later in a place
// with no line number to point at.
func parseEnvValue(raw string, lineNo int) (string, error) {
	if len(raw) < 2 {
		return raw, nil
	}
	quote := raw[0]
	if quote != '"' && quote != '\'' {
		return raw, nil
	}
	if raw[len(raw)-1] != quote {
		return "", errEnvLine(lineNo, "has an unterminated quoted value")
	}
	inner := raw[1 : len(raw)-1]
	if strings.ContainsRune(inner, rune(quote)) && quote == '\'' {
		return "", errEnvLine(lineNo, "has an unterminated quoted value")
	}
	if quote == '\'' {
		return inner, nil
	}
	return unescapeDoubleQuoted(inner), nil
}

// unescapeDoubleQuoted expands the escape set a double-quoted vault.env
// value may use. An unrecognised escape keeps its backslash, matching how
// a shell treats it.
func unescapeDoubleQuoted(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '\\', '"':
			b.WriteByte(s[i])
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// errEnvLine builds a parse refusal that names the line number and the
// problem, and never the line's content.
func errEnvLine(lineNo int, problem string) error {
	return cascade.New(cascade.KindInvalidInput,
		"secrets: vault.env line "+strconv.Itoa(lineNo)+" "+problem+" (its content is withheld: it may be a secret)")
}

// ImportReport summarises an import. It carries counts and names only.
type ImportReport struct {
	// Parsed is the number of KEY=value lines read from the file.
	Parsed int
	// Created is the number of names that did not exist before.
	Created int
	// Updated is the number of existing names overwritten in place.
	Updated int
	// Names lists every name touched, sorted. Never a value.
	Names []string
}

// Import parses data and loads every entry into b, overwriting in place. It
// is idempotent: importing the same file twice succeeds both times and
// leaves one entry per key, with the second run reporting them as updates.
//
// A parse failure imports nothing: the whole file is parsed before the
// first write, so a malformed file cannot leave the vault half-loaded.
func Import(ctx context.Context, b *Broker, data []byte) (ImportReport, error) {
	if b == nil {
		return ImportReport{}, cascade.New(cascade.KindInvalidInput, "secrets: import needs a broker")
	}
	entries, err := ParseVaultEnv(data)
	if err != nil {
		return ImportReport{}, err
	}
	report := ImportReport{Parsed: len(entries)}
	seen := map[string]bool{}
	for _, entry := range entries {
		result, serr := b.Set(ctx, entry.Name, entry.Value, SetUpdate)
		if serr != nil {
			return ImportReport{}, serr
		}
		if !seen[entry.Name] {
			seen[entry.Name] = true
			report.Names = append(report.Names, entry.Name)
			if result.Replaced {
				report.Updated++
			} else {
				report.Created++
			}
		}
	}
	report.Names = sortedNames(report.Names)
	return report, nil
}
