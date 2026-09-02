package runtime

import "fmt"

// Purpose: the schema_version migration frame every config.toml carries.
// Inputs: UpgradeSchema takes the raw decoded config document (a generic
//   TOML tree) and mutates it in place.
// Outputs: a SchemaUpgradeResult reporting the version transition and
//   whether anything actually changed; the caller (config.go's Load) is
//   responsible for the atomic file rewrite when Mutated is true.
// Constraints: deterministic and idempotent — a second run against an
//   already-current tree performs zero steps and reports Mutated=false
//   (T-1 acceptance criterion). A schema_version newer than this binary
//   understands is a hard typed error; downgrade is never attempted.
// SPORT: runtime/schema (ADD, placeholder per T-1 sport_updates).

// CurrentSchemaVersion is the schema_version every config.toml converges
// to after UpgradeSchema runs. P1 has only ever shipped one schema
// generation; a later ticket bumps this constant and appends a real
// migrationStep the day a structural change is needed.
const CurrentSchemaVersion = 1

// SchemaUpgradeResult reports what UpgradeSchema did to a config tree.
type SchemaUpgradeResult struct {
	FromVersion int
	ToVersion   int
	// Mutated is true iff the tree's schema_version was absent or older
	// than CurrentSchemaVersion, i.e. iff the caller must rewrite the
	// file. A file already at CurrentSchemaVersion yields Mutated=false.
	Mutated bool
}

// SchemaError reports a schema_version newer than this binary understands.
// Downgrading a config file is never attempted.
type SchemaError struct {
	FoundVersion   int
	CurrentVersion int
}

// Error implements the error interface.
func (e *SchemaError) Error() string {
	return fmt.Sprintf(
		"runtime: config schema_version %d is newer than this binary supports (current %d); upgrade cascade before loading this config",
		e.FoundVersion, e.CurrentVersion,
	)
}

// migrationStep transforms tree in place from one schema version to the
// next. Steps run in order, one at a time, so each step only ever bridges
// exactly one version.
type migrationStep struct {
	from int
	to   int
	run  func(tree map[string]interface{})
}

// migrationSteps is the ordered, deterministic upgrade chain. It is empty
// today: schema 1 is the only generation that has ever existed on disk, so
// the only migration this ticket needs (the "v0" case — no schema_version
// key present at all) upgrades to 1 by nothing more than stamping the
// version, which UpgradeSchema does unconditionally below. A later ticket
// appends a step here the day schema 2 needs a real structural rewrite.
var migrationSteps = []migrationStep{}

// UpgradeSchema detects tree's current schema_version (0 when the key is
// absent) and runs every applicable migration step in order up to
// CurrentSchemaVersion, mutating tree in place. It is idempotent: running
// it again against an already-current tree performs zero steps and
// reports Mutated=false.
func UpgradeSchema(tree map[string]interface{}) (SchemaUpgradeResult, error) {
	from := schemaVersionOf(tree)
	if from > CurrentSchemaVersion {
		return SchemaUpgradeResult{}, &SchemaError{FoundVersion: from, CurrentVersion: CurrentSchemaVersion}
	}
	if from == CurrentSchemaVersion {
		return SchemaUpgradeResult{FromVersion: from, ToVersion: from, Mutated: false}, nil
	}

	cur := from
	for _, step := range migrationSteps {
		if cur != step.from || cur >= CurrentSchemaVersion {
			continue
		}
		step.run(tree)
		cur = step.to
	}
	// No structural migration exists yet for any version below current;
	// the v0-to-1 transition is the version stamp itself.
	cur = CurrentSchemaVersion
	tree["schema_version"] = int64(cur)

	return SchemaUpgradeResult{FromVersion: from, ToVersion: cur, Mutated: true}, nil
}

// schemaVersionOf reads tree's schema_version key, tolerating the several
// numeric shapes a TOML decoder may hand back (int64 from a real decode,
// plain int or float64 from a hand-built test tree). Absent or
// non-numeric is treated as version 0 (the legacy/unversioned case).
func schemaVersionOf(tree map[string]interface{}) int {
	v, ok := tree["schema_version"]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	case float64:
		return int(n)
	default:
		return 0
	}
}
