package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for the two AGENTS.md serializers. What makes these targets real is
// not this package's opinion but the captured behaviour of the tools that
// consume them, so every target constant asserted here is DERIVED from a
// capture under testdata/goldens/ rather than restated from the source.
// Provenance for each capture is in testdata/README.md.

// readCapture reads one captured fixture.
func readCapture(t *testing.T, parts ...string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(append([]string{"testdata", "goldens"}, parts...)...))
	if err != nil {
		t.Fatalf("reading capture %v: %v", parts, err)
	}
	return string(raw)
}

// captureField returns the value of a "<key><spaces><value>" line.
func captureField(t *testing.T, capture, key string) string {
	t.Helper()
	for _, line := range strings.Split(capture, "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == key {
			return fields[1]
		}
	}
	t.Fatalf("the capture names no %q field", key)
	return ""
}

// agentsNameFromCapture pulls the instruction file name out of a capture,
// so the constant in the generator is checked against the tool's own word
// for it rather than against a second copy of itself.
func agentsNameFromCapture(t *testing.T, capture string) string {
	t.Helper()
	const marker = "AGENTS.md"
	if !strings.Contains(capture, marker) {
		t.Fatalf("the capture does not name %s at all", marker)
	}
	return marker
}

// TestCXTargetMatchesItsCapture derives the first target's file names from
// the captured invocation of the real tool.
func TestCXTargetMatchesItsCapture(t *testing.T) {
	ingest := readCapture(t, "cx", "prompt-input.capture.txt")
	name := agentsNameFromCapture(t, ingest)
	if cxTarget.tierName != name {
		t.Errorf("per-tier name = %q, the tool's capture says %q", cxTarget.tierName, name)
	}
	home := captureField(t, readCapture(t, "cx", "codex-home.capture.txt"), "CODEX_HOME")
	want := strings.TrimPrefix(home, "~/") + "/" + name
	if cxTarget.globalName != want {
		t.Errorf("global name = %q, derived from the capture: %q", cxTarget.globalName, want)
	}
}

// TestCXCaptureShowsMostGeneralFirst is the reason the emission order is
// what it is: the tool concatenates the global file ahead of the repo's,
// and the repo's ahead of the nested one, so cascade's own ordering must
// agree or the two disagree about which instruction wins.
func TestCXCaptureShowsMostGeneralFirst(t *testing.T) {
	ingest := readCapture(t, "cx", "prompt-input.capture.txt")
	global, repo, nested := strings.Index(ingest, "GCIMARK"), strings.Index(ingest, "PRIMARK"), strings.Index(ingest, "PAIMARK")
	if global < 0 || repo < 0 || nested < 0 {
		t.Fatalf("the capture is missing a tier marker (global=%d repo=%d nested=%d)", global, repo, nested)
	}
	if global >= repo || repo >= nested {
		t.Fatalf("the capture's order is global=%d repo=%d nested=%d, not most-general-first", global, repo, nested)
	}
}

// TestCXCaptureShowsVerbatimIngestion pins the format claim itself: the
// tool reproduces the file's bytes with nothing added inside them, which is
// why the generator emits plain Markdown and no envelope of its own.
func TestCXCaptureShowsVerbatimIngestion(t *testing.T) {
	ingest := readCapture(t, "cx", "prompt-input.capture.txt")
	for _, want := range []string{"# GCI\nglobal tier marker GCIMARK", "# PAI\npai tier marker PAIMARK"} {
		if !strings.Contains(ingest, want) {
			t.Errorf("the capture does not carry %q verbatim", want)
		}
	}
}

// TestOCTargetMatchesItsCapture derives the second target's file names from
// the shipped instruction-path resolver and the tool's own reported paths.
func TestOCTargetMatchesItsCapture(t *testing.T) {
	resolver := readCapture(t, "oc", "instruction-systempaths.capture.txt")
	name := agentsNameFromCapture(t, resolver)
	if ocTarget.tierName != name {
		t.Errorf("per-tier name = %q, the resolver says %q", ocTarget.tierName, name)
	}
	if !strings.Contains(resolver, `["`+name+`"`) {
		t.Errorf("the resolver does not put %s first in its candidate list", name)
	}
	if !strings.Contains(resolver, `join(r.config,"`+name+`")`) {
		t.Errorf("the resolver does not read the global file from its config directory")
	}
	cfg := captureField(t, readCapture(t, "oc", "debug-paths.capture.txt"), "config")
	want := strings.TrimPrefix(cfg, "<HOME>/") + "/" + name
	if ocTarget.globalName != want {
		t.Errorf("global name = %q, derived from the captures: %q", ocTarget.globalName, want)
	}
}

// TestAgentsWritersNameEveryTier checks the per-role file naming for both
// targets, including the deliberate difference at the global tier: neither
// tool reads a bare AGENTS.md in the home directory, so emitting one there
// would write a file the harness ignores.
func TestAgentsWritersNameEveryTier(t *testing.T) {
	cases := []struct {
		name   string
		gen    HarnessGenerator
		global string
	}{
		{"CX", &CXInstructionWriter{}, cxTarget.globalName},
		{"OC", &OCInstructionWriter{}, ocTarget.globalName},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files, err := tc.gen.Generate(mergeGoldenCorpus(t))
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			for _, f := range files {
				want := agentsFileName
				if f.Role == TierGCI {
					want = tc.global
				}
				if f.Name != want {
					t.Errorf("tier %s: Name = %q, want %q", f.Role, f.Name, want)
				}
			}
		})
	}
}

// TestAgentsTargetNamesCarryNoMachinePath is the canary for the one thing
// these constants could get wrong in a way a user would only find after
// committing it: an absolute path from the machine that built them.
func TestAgentsTargetNamesCarryNoMachinePath(t *testing.T) {
	for _, target := range []agentsTarget{cxTarget, ocTarget} {
		for _, name := range []string{target.tierName, target.globalName} {
			if filepath.IsAbs(name) || strings.HasPrefix(name, "~") {
				t.Errorf("target %s name %q is not relative to a tier root", target.id, name)
			}
			if unrenderable(name) != "" {
				t.Errorf("target %s name %q trips the content guard (%s)",
					target.id, name, unrenderable(name))
			}
		}
	}
}
