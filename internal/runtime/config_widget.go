// Purpose: the `[widget]` table parser behind Load (config.go):
// 08-INIT-CONFIG-SPEC.md §3's show_project_names scalar (R-21.200, per
// P1-E38-W8-S74-T1's status.widget contract). Split out as its own
// sibling file per R-14.117's established remedy — config_economics.go
// and config_fleet.go do the same — rather than growing config.go past
// its 300-line cap.
//
// CONTRACT DEVIATION (R-16.79, recorded, same reasoning as
// config_economics.go's own identical note). Reload class (hot vs cold)
// is expressed structurally by hotreload.go's coldSections list, which
// [widget] is simply never added to — no separate registration step is
// needed for it to be hot-reloadable; this file's own existence and
// config_sections.go's dispatch call are the registration.
//
// Inputs: the decoded generic config tree (env overrides already applied).
// Outputs: a widgetSection, or a typed *ConfigError for an unknown
// top-level key or a type mismatch.
// Constraints: show_project_names defaults to false (R-21.200: opaque ids
// and neutral labels are the safe default; a user opts INTO real names).
// SPORT: runtime/config-widget (ADD, P1-E38-W8-S74-T1).

package runtime

// widgetSection is the [widget] table's typed view.
type widgetSection struct {
	ShowProjectNames bool
}

// defaultShowProjectNames is 08 §3's shipped default for
// [widget].show_project_names.
const defaultShowProjectNames = false

// widgetTopKeys is the closed set of keys a [widget] table recognises.
var widgetTopKeys = map[string]bool{"show_project_names": true}

// parseWidgetSection type-checks tree's [widget] table.
func parseWidgetSection(tree map[string]interface{}) (widgetSection, error) {
	sec := widgetSection{ShowProjectNames: defaultShowProjectNames}
	raw, ok := tree["widget"].(map[string]interface{})
	if !ok {
		return sec, nil
	}
	for k := range raw {
		if !widgetTopKeys[k] {
			return widgetSection{}, &ConfigError{Field: "widget." + k, Reason: "unrecognised key in [widget]"}
		}
	}
	v, ok := raw["show_project_names"]
	if !ok {
		return sec, nil
	}
	b, ok := v.(bool)
	if !ok {
		return widgetSection{}, &ConfigError{Field: "widget.show_project_names", Reason: "must be a boolean"}
	}
	sec.ShowProjectNames = b
	return sec, nil
}
