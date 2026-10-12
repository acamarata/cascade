// Purpose: TestDualTagMapping proves the §D-33 artifact-to-module tag map
//
//	and its refusals (identity and message, never errors.Is alone, which
//	matches any error of the same Kind).
//
// Inputs:  literal tags. Outputs: pass/fail.
// Constraints: pure, no I/O.
//
// SPORT: internal/buildinfo — release tag mapping (contract:signed-release-artifacts).
package buildinfo

import (
	"errors"
	"strconv"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"golang.org/x/mod/semver"
)

func TestDualTagMapping(t *testing.T) {
	for in, want := range map[string]string{
		"v2.0.0":        "v0.0.0",
		"v2.0.0-rc.1":   "v0.0.0-rc.1",
		"v2.3.4":        "v0.3.4",
		"v2.10.20-rc.3": "v0.10.20-rc.3",
		"v2.1.0-0a.01x": "v0.1.0-0a.01x",
		"v2.1.0-beta-2": "v0.1.0-beta-2",
	} {
		got, err := ModuleTagFor(in)
		if err != nil || got != want || !semver.IsValid(got) {
			t.Errorf("ModuleTagFor(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"v3.0.0", "v2.0", "2.0.0", "v1.2.3", "v0.0.0", "", "v2.0.0+meta",
		"v2.01.0", "v2.0.0-", "v2.0.0-rc..1", " v2.0.0", "v2.0.0\n", "V2.0.0",
		"v2.0.0-01", "v2.0.0-rc.01", "v2.0.0-00", "v2.0.0-rc.1.",
	} {
		got, err := ModuleTagFor(in)
		var ce *cascade.Error
		if got != "" || !errors.As(err, &ce) || ce.Kind != cascade.KindInvalidInput {
			t.Errorf("ModuleTagFor(%q) = %q, %v; want KindInvalidInput refusal", in, got, err)
			continue
		}
		if want := "buildinfo: artifact tag " + strconv.Quote(in) + " is not v2.Y.Z[-prerelease]"; ce.Msg != want {
			t.Errorf("ModuleTagFor(%q) message %q, want %q", in, ce.Msg, want)
		}
	}
}
