//go:build !darwin

package service

// Purpose: the non-macOS refusal for the widget LaunchAgent verbs. The menu-
//   bar widget exists only on macOS, so install, uninstall and status refuse
//   with the typed KindUnsupported error naming the verb.
// Inputs: none read. Outputs: zero reports and a KindUnsupported error.
// Constraints: imports nothing filesystem or exec related.
// SPORT: internal/daemon/service (P1-WID-04).

import "github.com/acamarata/cascade/pkg/cascade"

// widgetUnsupportedHint is the shared tail of every refusal.
const widgetUnsupportedHint = "the menu-bar widget is macOS-only; this platform has no widget LaunchAgent"

// NewWidgetInstaller returns the refusal installer.
func NewWidgetInstaller() WidgetInstaller { return unsupportedWidgetInstaller{} }

type unsupportedWidgetInstaller struct{}

func widgetRefusal(verb string) error {
	return cascade.New(cascade.KindUnsupported, "widget "+verb+": "+widgetUnsupportedHint)
}

// Install always refuses.
func (unsupportedWidgetInstaller) Install(WidgetConfig) (WidgetInstallReport, error) {
	return WidgetInstallReport{}, widgetRefusal("install")
}

// Uninstall always refuses.
func (unsupportedWidgetInstaller) Uninstall(WidgetConfig) (DeltaReport, error) {
	return DeltaReport{}, widgetRefusal("uninstall")
}

// Status always refuses.
func (unsupportedWidgetInstaller) Status(WidgetConfig) (WidgetStatusReport, error) {
	return WidgetStatusReport{}, widgetRefusal("status")
}
