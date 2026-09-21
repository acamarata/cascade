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
	// economics is the [fleet.economics] block (P1-E41-W9-S79-T2), same
	// deviation as fleetAccounts above -- see config_economics.go.
	economics economicsSection
	// widget is the [widget] block (P1-E38-W8-S74-T1), same deviation as
	// fleetAccounts above -- see config_widget.go.
	widget widgetSection
	// plugins is the [plugins] block (P1-E15-W4-S33-T4), same deviation
	// as fleetAccounts above -- see config_plugins.go.
	plugins pluginsSection
	// ciPolicy is the [ci.policy] block and ciLocal the [ci.local] block
	// (P1-E25-W5-S51-T5), same deviation as fleetAccounts above -- see
	// config_ci.go.
	ciPolicy ciPolicySection
	ciLocal  ciLocalSection
	// ciWatch is the ci.watch key (P1-E25-W5-S51-T4), same deviation as
	// fleetAccounts above -- see config_ci_watch.go.
	ciWatch ciWatchSection
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
	if s.economics, err = parseEconomicsSection(tree); err != nil {
		return configSections{}, err
	}
	if s.widget, err = parseWidgetSection(tree); err != nil {
		return configSections{}, err
	}
	if s.plugins, err = parsePluginsSection(tree); err != nil {
		return configSections{}, err
	}
	if s.ciPolicy, err = parseCIPolicySection(tree); err != nil {
		return configSections{}, err
	}
	if s.ciLocal, err = parseCILocalSection(tree); err != nil {
		return configSections{}, err
	}
	if s.ciWatch, err = parseCIWatchSection(tree); err != nil {
		return configSections{}, err
	}
	return s, nil
}
