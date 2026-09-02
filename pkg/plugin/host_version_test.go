// Purpose: coverage for R-14.128 — host_version's semver core is validated
// as strictly as version's (no leading zeros in any numeric component),
// and the range forms that remain unsupported (npm-style "||" alternation,
// "1.x" wildcards) are proven rejected, matching what Manifest.HostVersion's
// godoc now documents as known gaps rather than a silent rejection.
// Constraints: no network calls (Art.7.2); no writes at all.
package plugin_test

import (
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
)

// TestValidate_HostVersionLeadingZero is the R-14.128 regression case:
// version = "01.0.0" was already rejected before this fix; host_version =
// "01.2.0" was NOT — the asymmetry this ticket closes.
func TestValidate_HostVersionLeadingZero(t *testing.T) {
	m := validManifest()
	m.HostVersion = "01.2.0"
	errs := plugin.Validate(m)
	if !hasErrCode(errs, "host_version", plugin.ErrCodeMalformedVersion) {
		t.Errorf("Validate(host_version=%q) = %+v, want ErrCodeMalformedVersion", m.HostVersion, errs)
	}
}

// TestValidate_HostVersionUnsupportedForms covers one case of each
// documented-unsupported range form named on Manifest.HostVersion's godoc.
func TestValidate_HostVersionUnsupportedForms(t *testing.T) {
	cases := []string{
		"1.0.0 || 2.0.0", // npm-style alternation
		"1.x",            // wildcard core segment
	}
	for _, hv := range cases {
		t.Run(hv, func(t *testing.T) {
			m := validManifest()
			m.HostVersion = hv
			errs := plugin.Validate(m)
			if !hasErrCode(errs, "host_version", plugin.ErrCodeMalformedVersion) {
				t.Errorf("Validate(host_version=%q) = %+v, want ErrCodeMalformedVersion", hv, errs)
			}
		})
	}
}

// TestValidate_HostVersionStillAcceptsSupportedForms is a non-regression
// check: tightening leading-zero strictness must not reject any
// previously-supported range form.
func TestValidate_HostVersionStillAcceptsSupportedForms(t *testing.T) {
	cases := []string{"*", ">=2.0.0", "^1.2.0", "~1.2", ">=1.0.0 <2.0.0", "1"}
	for _, hv := range cases {
		t.Run(hv, func(t *testing.T) {
			m := validManifest()
			m.HostVersion = hv
			if errs := plugin.Validate(m); len(errs) != 0 {
				t.Errorf("Validate(host_version=%q) = %+v, want no errors", hv, errs)
			}
		})
	}
}
