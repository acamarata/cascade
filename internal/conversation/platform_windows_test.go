//go:build windows

// Purpose: the windows-only unit test for RefuseSSEEmbedded. This file
//   compiles ONLY on GOOS=windows; TestRefuseSSEOnEmbedded (sse_test.go)
//   is the portable test that runs on every platform, matching
//   internal/fleet/resume/platform_windows_test.go's precedent.
// SPORT: internal.conversation.sse/ADDED (tests) (P1-E20-W5-S43-T2).

package conversation

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestRefuseSSEEmbedded(t *testing.T) {
	if err := RefuseSSEEmbedded(); !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("RefuseSSEEmbedded() = %v, want KindUnsupported", err)
	}
}
