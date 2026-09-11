//go:build windows

// Purpose: the windows-only unit test for RefuseDaemonlessResume. This
//   file compiles ONLY on GOOS=windows (see platform_windows.go's doc
//   comment); TestResumeWindowsTier2Refusal (resume_test.go) is the
//   portable test that runs on every platform, per the ticket's own
//   "runs on all platforms" requirement.
// SPORT: internal.fleet.resume.ResumeManager/ADDED (tests) (P1-E13-W3-S27-T2).

package resume

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestRefuseDaemonlessResume(t *testing.T) {
	if err := RefuseDaemonlessResume(); !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("RefuseDaemonlessResume() = %v, want KindUnsupported", err)
	}
}
