package runtime

// Purpose: Validate, the one gate every config write path runs on a decoded
// tree before touching disk (ConfigWriter.Set/Unset, ApplyDiff, the ci.watch
// writer, `cascade config set`).
// Outputs: nil, or the first section's typed refusal.
// Constraints: a check added here covers every write path at once.
// SPORT: runtime/config-write-verbs (P1-SEC-30, AUD-027).

// Validate type-checks [elevation] and [logging] and refuses a newer schema.
func Validate(tree map[string]interface{}) error {
	if _, err := parseElevationSection(tree); err != nil {
		return err
	}
	if _, err := parseLoggingSection(tree, func(string, ...interface{}) {}); err != nil {
		return err
	}
	if err := validateFleetDiscovery(tree); err != nil {
		return err
	}
	if from := schemaVersionOf(tree); from > CurrentSchemaVersion {
		return &SchemaError{FoundVersion: from, CurrentVersion: CurrentSchemaVersion}
	}
	return nil
}
