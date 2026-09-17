package plugins

// Purpose: the tier-2 half of the shared-instruction-file rule
//   (P1-E16-W4-S35-T13, R-14.267) -- on a platform whose harness paths
//   this build does not resolve, an uninstall must still run, keeping
//   every possibly-shared file with a reason that names the limit rather
//   than asserting an install nobody verified.
// Constraints: these drive a STATED detector, so the tier-2 path is
//   exercised on every platform and not only on the one that has the
//   limit. No network, no real home (Art.7).
// SPORT: internal/plugins tier-2 shared paths (ADD) -- P1-E16-W4-S35-T13.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	casctx "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/plugins/codex"
	"github.com/acamarata/cascade/plugins/opencode"
)

// refusingDetector answers with err, so the tier-2 path can be driven on
// every platform rather than only on the one that has the limit.
type refusingDetector struct{ err error }

func (d refusingDetector) Detect(context.Context) ([]casctx.HarnessState, error) {
	return nil, d.err
}

// TestATier2PlatformKeepsTheSharedFileAndSaysWhy pins the third answer.
//
// Returning the detector's refusal made every uninstall on a tier-2
// platform fail before removing anything — eight red tests on
// windows/amd64. "I could not tell" must not cost the whole operation; it
// costs one kept file, with a reason an operator can act on.
func TestATier2PlatformKeepsTheSharedFileAndSaysWhy(t *testing.T) {
	dir := crossHarnessProject(t)
	seedHarnessRoots(t)

	claimed, err := sharedPathsFor(context.Background(),
		refusingDetector{casctx.ErrHarnessDetectionUnsupported}, casctx.HarnessCodex, dir)
	if err != nil {
		t.Fatalf("a tier-2 platform must not fail the uninstall: %v", err)
	}
	why := claimed[filepath.Join(dir, "AGENTS.md")]
	if why == "" {
		t.Fatalf("the shared file is not claimed on a tier-2 platform: %v", claimed)
	}
	// The whole point of moving the reason to the host: a tier-2 platform
	// knows the file is shared and does NOT know anything is installed, so
	// it must not say so.
	if strings.Contains(why, "is installed") {
		t.Errorf("tier-2 reason claims an installation nobody verified: %q", why)
	}
	if !strings.Contains(why, "tier-2") {
		t.Errorf("tier-2 reason does not name the platform limit: %q", why)
	}
}

// TestATier2PlatformStillClaimsEveryOtherHarness covers the harness this
// project's own adapter never asks about — cascade-claude has no uninstall
// subcommand, so its generator is only ever reached through here.
func TestATier2PlatformStillClaimsEveryOtherHarness(t *testing.T) {
	dir := crossHarnessProject(t)
	seedHarnessRoots(t)

	claimed, err := sharedPathsFor(context.Background(),
		refusingDetector{casctx.ErrHarnessDetectionUnsupported}, casctx.HarnessCodex, dir)
	if err != nil {
		t.Fatalf("computing the tier-2 set: %v", err)
	}
	var sawClaude bool
	for path := range claimed {
		if strings.HasSuffix(path, "CLAUDE.md") {
			sawClaude = true
		}
	}
	if !sawClaude {
		t.Errorf("the tier-2 set names no cascade-claude file: %v; "+
			"a harness left out of the conservative set is one whose file gets deleted", claimed)
	}
}

// TestANonTier2DetectorErrorStillFailsTheUninstall is the other half, and
// it is what keeps the fallback from swallowing every failure: only the
// documented tier-2 refusal degrades.
func TestANonTier2DetectorErrorStillFailsTheUninstall(t *testing.T) {
	boom := errors.New("the detector could not read the config root")
	_, err := sharedPathsFor(context.Background(),
		refusingDetector{boom}, casctx.HarnessCodex, crossHarnessProject(t))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the detector's own error; a real failure must not degrade to a guess", err)
	}
}

// TestAGeneratorFailureIsNotAnEmptySharedSet covers the path that matters
// most and shows least: a generator that errors must abort the
// computation. Returning what it had so far would hand the adapter a
// PARTIAL set, which reads exactly like "nothing else claims this file"
// for whatever it did not reach.
func TestAGeneratorFailureIsNotAnEmptySharedSet(t *testing.T) {
	dir := crossHarnessProject(t)
	boom := errors.New("the generator could not read a tier file")
	prev := codex.Generate
	codex.Generate = func(context.Context, string) ([]codex.GeneratedFile, error) { return nil, boom }
	t.Cleanup(func() { codex.Generate = prev })

	for _, tc := range []struct {
		name     string
		detector harnessDetector
	}{
		{"detection worked", statedDetector{[]casctx.HarnessKind{casctx.HarnessCodex}}},
		{"tier-2", refusingDetector{casctx.ErrHarnessDetectionUnsupported}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := sharedPathsFor(
				context.Background(), tc.detector, casctx.HarnessOpenCode, dir,
			); !errors.Is(err, boom) {
				t.Fatalf("err = %v, want the generator's own error", err)
			}
		})
	}
}

// TestTheWiredResolversAnswerForTheirOwnHarness drives the package-level
// seams the init block installs, rather than sharedPathsFor directly.
//
// Wiring that is only ever exercised through the function it wraps is
// wiring nobody has proved reaches anything — and the argument each
// closure passes (which harness is being REMOVED) is exactly the kind of
// thing a copy-paste gets wrong in the second one.
func TestTheWiredResolversAnswerForTheirOwnHarness(t *testing.T) {
	dir := crossHarnessProject(t)
	seedHarnessRoots(t)

	codexSet, err := codex.ResolveSharedPaths(context.Background(), dir)
	if err != nil {
		t.Fatalf("the cascade-codex resolver: %v", err)
	}
	opencodeSet, err := opencode.ResolveSharedPaths(context.Background(), dir)
	if err != nil {
		t.Fatalf("the cascade-opencode resolver: %v", err)
	}

	// Each resolver excludes ITS OWN harness's files, so the set a
	// resolver returns must not name a path only its own generator
	// produces. The shared project file is in both, which is the point.
	codexHome := filepath.Join(crossHarnessHome, ".codex", "AGENTS.md")
	if codexSet[codexHome] != "" {
		t.Errorf("the cascade-codex resolver claims codex's own file %s: %v", codexHome, codexSet)
	}
	opencodeHome := filepath.Join(crossHarnessHome, ".config", "opencode", "AGENTS.md")
	if opencodeSet[opencodeHome] != "" {
		t.Errorf("the cascade-opencode resolver claims opencode's own file %s: %v", opencodeHome, opencodeSet)
	}
}
