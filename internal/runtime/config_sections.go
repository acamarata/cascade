package runtime

// Purpose: the section-parse helper Load delegates to. Split out of
//   config.go because that file sat at exactly the 300-line cap and this
//   extraction, made to satisfy the funlen linter, pushed it to 317
//   (R-14.204). Two gates disagreeing about the same edit is the reason
//   both must run before a commit, not one.
// Inputs: the decoded config tree and the warn sink.
// Outputs: the four parsed sections, or the first error encountered.
// Constraints: parse order is fixed so a malformed section is reported as
//   itself rather than as a later section's missing input.
// SPORT: internal.runtime.parseConfigSections/ADDED (T0, R-14.201).

// configSections groups the four independent section parses Load performs.
// Extracted from Load purely so each stays readable: Load itself had grown
// past the 50-line funlen gate, and four near-identical parse-and-bail
// blocks were the bulk of it.
type configSections struct {
	elevation     elevationSection
	logging       loggingSection
	retrieval     retrievalSection
	fusionEnabled bool
	// fleetAccounts is R-21.44's `[fleet.accounts."<id>"]` map (P1-E41-
	// W9-S80-T1), added to this dispatch struct rather than to files_scope's
	// literal config.go/schema.go pair alone -- see config_fleet.go's own
	// files_scope-deviation note.
	fleetAccounts map[string]FleetAccountConfig
}

// parseConfigSections runs each section parser in turn, returning on the
// first failure so a malformed section is reported as itself rather than
// as a later section's missing input.
func parseConfigSections(tree map[string]interface{}, warn func(string, ...interface{})) (configSections, error) {
	var s configSections
	var err error
	if s.elevation, err = parseElevationSection(tree); err != nil {
		return configSections{}, err
	}
	if s.logging, err = parseLoggingSection(tree, warn); err != nil {
		return configSections{}, err
	}
	if s.retrieval, err = parseRetrievalSection(tree); err != nil {
		return configSections{}, err
	}
	if s.fusionEnabled, err = resolveFusionEnabled(tree); err != nil {
		return configSections{}, err
	}
	if s.fleetAccounts, err = parseFleetAccountsSection(tree, warn); err != nil {
		return configSections{}, err
	}
	return s, nil
}
