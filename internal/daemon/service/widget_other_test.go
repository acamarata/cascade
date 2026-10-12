//go:build !darwin

// Purpose: the non-macOS refusal of the widget verbs.
// SPORT: internal/daemon/service (P1-WID-04).
package service

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestWidgetUnsupportedPlatformRefusal(t *testing.T) {
	inst := NewWidgetInstaller()
	_, installErr := inst.Install(WidgetConfig{})
	_, uninstallErr := inst.Uninstall(WidgetConfig{})
	_, statusErr := inst.Status(WidgetConfig{})
	for verb, err := range map[string]error{"install": installErr, "uninstall": uninstallErr, "status": statusErr} {
		if !cascade.HasKind(err, cascade.KindUnsupported) {
			t.Errorf("%s: err = %v, want KindUnsupported", verb, err)
			continue
		}
		if !strings.Contains(err.Error(), "widget "+verb+":") {
			t.Errorf("%s: error %q does not name the verb", verb, err)
		}
	}
}
