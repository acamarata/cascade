// Package build (this file) implements R-16.77's static ledger-identity
// gate: internal/storage/migrate's applied_migrations ledger now keys
// every row by (SetID, schema_version) rather than schema_version alone,
// so every production migrate.MigrationSet{...} composite literal must
// carry a non-empty SetID, and no two literals in DIFFERENT packages may
// claim the same one (a collision would let one set's rows mask the
// other's in the shared ledger table, exactly the defect class R-16.77
// retired schemaceilinggate*.go and runtimeReaderCeiling() for).
//
// MECHANICAL DERIVATION, no hand-maintained list on either side (the same
// shape schemaceilinggate.go used before R-16.77 retired it): every
// non-test .go file that imports internal/storage/migrate is parsed, its
// import's local alias resolved from its own import declarations, and
// every "<alias>.MigrationSet{...}" composite literal in it is inspected
// for a SetID key. A SetID whose value is a plain string literal (or a
// chain of "+"-concatenated string literals) is checked for a tree-wide
// duplicate against every other package's own resolvable SetID; a value
// that depends on a runtime argument (internal/storage/plugin_migrate.go's
// pluginSetID(pluginID), which folds in the caller-supplied plugin id) is
// exempt from the duplicate check by construction — it cannot collide
// with a different package's compile-time literal, and two plugins are
// kept apart by their own distinct ids, not by this gate.
//
// WHAT THIS GATE CANNOT CATCH (per this package's convention, every gate
// states its own blind spot):
//   - A SetID built from anything other than a bare string literal or a
//     literal-only "+" chain (e.g. a variable, a fmt.Sprintf call) is
//     invisible to the duplicate check, exactly like pluginSetID above.
//     Presence (the literal has a SetID key at all) is still enforced.
//   - A SetID that collides with a LEGACY on-disk row's empty string
//     ("") is not this gate's concern: R-16.77's own migration path
//     treats "" as the reserved legacy sentinel, and no production
//     literal may set SetID to "" (an empty resolved string is treated
//     the same as "not statically resolvable" here and skips the
//     duplicate check, but the presence check above still requires the
//     key to exist syntactically).
//
// SPORT: internal.build.CheckLedgerIdentity/ADDED (R-16.77).
package build

import (
	"fmt"
	"go/ast"
	"go/token"
	"strconv"
)

// migrationSetImportPath is internal/storage/migrate's full import path.
// Resolved against each file's OWN import declarations (never assumed),
// see migrateAliasForFile.
const migrationSetImportPath = "github.com/acamarata/cascade/internal/storage/migrate"

// LedgerIdentityViolation is one ledger-identity defect this gate found:
// either a MigrationSet literal with no SetID key ("missing"), or one
// whose resolvable SetID string duplicates an earlier literal claimed by
// a DIFFERENT package ("duplicate").
type LedgerIdentityViolation struct {
	File      string
	Line      int
	Kind      string // "missing" or "duplicate"
	SetID     string // "" for Kind == "missing"
	OtherFile string // Kind == "duplicate" only: the first claimant
	OtherLine int
}

// String renders one violation for gate failure output.
func (v LedgerIdentityViolation) String() string {
	if v.Kind == "missing" {
		return fmt.Sprintf("%s:%d: migrate.MigrationSet{...} has no SetID (R-16.77 per-set ledger identity)", v.File, v.Line)
	}
	return fmt.Sprintf("%s:%d: SetID %q duplicates %s:%d (R-16.77 requires every MigrationSet's SetID to be unique tree-wide)",
		v.File, v.Line, v.SetID, v.OtherFile, v.OtherLine)
}

// migrateAliasForFile resolves the local identifier f's import of
// internal/storage/migrate is bound to ("" if the file never imports
// it), reading the file's own import declarations rather than assuming
// the conventional "migrate" name.
func migrateAliasForFile(f *ast.File) string {
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != migrationSetImportPath {
			continue
		}
		if imp.Name != nil && imp.Name.Name != "_" && imp.Name.Name != "." {
			return imp.Name.Name
		}
		return "migrate"
	}
	return ""
}

// resolveStringLiteral resolves expr to a compile-time string when it is
// a plain string literal or a chain of "+"-concatenated string literals —
// the only shape a production SetID value takes beyond a bare literal
// anywhere in this tree today. Any other shape (a variable, a call) is
// deliberately NOT resolved: see this file's package doc for why that is
// correct rather than a gap.
func resolveStringLiteral(expr ast.Expr) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(e.Value)
		return v, err == nil
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		left, ok := resolveStringLiteral(e.X)
		if !ok {
			return "", false
		}
		right, ok := resolveStringLiteral(e.Y)
		if !ok {
			return "", false
		}
		return left + right, true
	default:
		return "", false
	}
}
