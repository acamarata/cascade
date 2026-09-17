package context

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// probeFor returns a PathProbe that reports exactly the given paths as
// present. A map rather than a prefix rule, so a test states which roots
// exist and nothing infers a fourth.
func probeFor(present ...string) PathProbe {
	set := map[string]bool{}
	for _, p := range present {
		set[p] = true
	}
	return func(path string) bool { return set[path] }
}

// envFor returns an EnvFunc over a fixed map.
func envFor(pairs map[string]string) EnvFunc {
	return func(key string) string { return pairs[key] }
}

// TestHarnessDetect walks the three states a machine can be in, on both
// real platform families.
//
// Every case asserts the FULL list, not just the detected entries: a
// detector that dropped absent harnesses would make "codex is not
// installed" and "this build forgot codex" indistinguishable in the one
// surface built to tell an operator what is set up.
// harnessDetectCase is one machine's environment and what a correct
// detector reports for it.
type harnessDetectCase struct {
	name     string
	goos     string
	env      EnvFunc
	probe    PathProbe
	detected []HarnessKind
}

// harnessDetectCases is the table TestHarnessDetect walks. It lives out
// here so the test body stays the assertions and this stays the machines.
func harnessDetectCases() []harnessDetectCase {
	homeEnv := envFor(map[string]string{"HOME": "/h"})
	claudeRoot := filepath.Join("/h", ".claude")
	codexRoot := filepath.Join("/h", ".codex")
	desktopAppRoot := filepath.Join("/h", "Library", "Application Support", "Claude")

	return []harnessDetectCase{
		{"nothing installed", "darwin", homeEnv, probeFor(), nil},
		{"one installed", "darwin", homeEnv, probeFor(claudeRoot), []HarnessKind{HarnessClaude}},
		{"partial install", "darwin", homeEnv, probeFor(claudeRoot, codexRoot),
			[]HarnessKind{HarnessClaude, HarnessCodex}},
		// The desktop application of the same brand keeps an Electron
		// profile under Application Support. It is a different product
		// with none of our configuration in it, and a machine that has
		// only it has no CLI harness installed.
		{"the desktop app is not the CLI harness", "darwin", homeEnv, probeFor(desktopAppRoot), nil},
		// The harnesses that keep a dotfile root do NOT honour XDG, so a
		// machine that sets XDG_CONFIG_HOME still finds them under HOME.
		{"xdg relocates only the harness that uses it", "linux",
			envFor(map[string]string{"XDG_CONFIG_HOME": "/x", "HOME": "/h"}),
			probeFor(filepath.Join("/x", "opencode"), codexRoot),
			[]HarnessKind{HarnessCodex, HarnessOpenCode}},
		{"linux without xdg falls back to home", "linux", homeEnv,
			probeFor(filepath.Join("/h", ".codex")), []HarnessKind{HarnessCodex}},
		{"an unlisted GOOS follows the xdg rule", "freebsd", homeEnv,
			probeFor(filepath.Join("/h", ".config", "opencode")), []HarnessKind{HarnessOpenCode}},
		// Each harness's own override wins over both, which is how a
		// second installation of one harness is found at all.
		{"an override relocates one harness", "darwin",
			envFor(map[string]string{"HOME": "/h", OverrideVarFor(HarnessClaude): "/elsewhere/cfg"}),
			probeFor("/elsewhere/cfg", codexRoot),
			[]HarnessKind{HarnessClaude, HarnessCodex}},
		{"an override in force means the default root is not probed", "darwin",
			envFor(map[string]string{"HOME": "/h", OverrideVarFor(HarnessClaude): "/elsewhere/cfg"}),
			probeFor(claudeRoot), nil},
	}
}

func TestHarnessDetect(t *testing.T) {
	for _, tc := range harnessDetectCases() {
		t.Run(tc.name, func(t *testing.T) {
			states, err := NewPathDetector(tc.goos, tc.env, tc.probe).Detect(context.Background())
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if len(states) != len(SupportedHarnesses()) {
				t.Fatalf("Detect reported %d harnesses, want all %d", len(states), len(SupportedHarnesses()))
			}
			for i, kind := range SupportedHarnesses() {
				if states[i].Kind != kind {
					t.Fatalf("states[%d].Kind = %q, want %q (order is part of the contract)", i, states[i].Kind, kind)
				}
				if states[i].InstallPath == "" {
					t.Errorf("%q reports no install path; a false negative is undebuggable without one", kind)
				}
			}
			got := DetectedKinds(states)
			if len(got) != len(tc.detected) {
				t.Fatalf("detected %v, want %v", got, tc.detected)
			}
			for i := range got {
				if got[i] != tc.detected[i] {
					t.Fatalf("detected %v, want %v", got, tc.detected)
				}
			}
		})
	}
}

// TestHarnessDetectRefusesWindows is Art.5's asserted refusal. An empty
// list would be a false negative an operator would act on, so the
// platform this build cannot resolve says so.
func TestHarnessDetectRefusesWindows(t *testing.T) {
	states, err := NewPathDetector("windows", envFor(map[string]string{"APPDATA": `C:\d`}), probeFor()).
		Detect(context.Background())
	if !errors.Is(err, ErrHarnessDetectionUnsupported) {
		t.Fatalf("err = %v, want ErrHarnessDetectionUnsupported", err)
	}
	if states != nil {
		t.Fatalf("a refusal still returned %d states", len(states))
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Fatalf("kind = %v (typed %t), want KindUnsupported", kind, ok)
	}
}

// TestHarnessDetectRefusesAnUnresolvableEnvironment covers the two ways a
// config root cannot be resolved. Both are refusals, not empty results.
func TestHarnessDetectRefusesAnUnresolvableEnvironment(t *testing.T) {
	for name, env := range map[string]EnvFunc{
		"darwin with no HOME": envFor(map[string]string{}),
		"linux with neither":  envFor(map[string]string{}),
	} {
		t.Run(name, func(t *testing.T) {
			goos := "darwin"
			if strings.HasPrefix(name, "linux") {
				goos = "linux"
			}
			if _, err := NewPathDetector(goos, env, probeFor()).Detect(context.Background()); err == nil {
				t.Fatal("detection succeeded with no resolvable home")
			}
		})
	}
}

// TestHarnessDetectRefusesAnUnbuiltDetector pins the nil-seam case as a
// refusal rather than a panic in a hook or a doctor row.
func TestHarnessDetectRefusesAnUnbuiltDetector(t *testing.T) {
	if _, err := NewPathDetector("darwin", nil, nil).Detect(context.Background()); err == nil {
		t.Fatal("a detector with no environment and no probe reported success")
	}
}

// TestHarnessDetectHonoursACancelledContext proves detection returns
// promptly rather than walking the table anyway.
func TestHarnessDetectHonoursACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewPathDetector("darwin", envFor(map[string]string{"HOME": "/h"}), probeFor()).Detect(ctx); err == nil {
		t.Fatal("detection ran to completion on a cancelled context")
	}
}

// TestThePathTableCoversEverySupportedHarness asserts the data and the
// enumeration agree. A harness added to one and not the other would
// detect as a hard error on every machine.
func TestThePathTableCoversEverySupportedHarness(t *testing.T) {
	for _, kind := range SupportedHarnesses() {
		if _, ok := harnessConfigDirs[kind]; !ok {
			t.Errorf("no config directory is known for supported harness %q", kind)
		}
	}
	if len(harnessConfigDirs) != len(SupportedHarnesses()) {
		t.Errorf("the path table has %d rows for %d supported harnesses",
			len(harnessConfigDirs), len(SupportedHarnesses()))
	}
}

// TestWithDriftReportsOnlyDetectedHarnesses pins the rule that keeps a
// drift list useful. Cascade generates instruction files for all three
// harnesses regardless of what is installed, so reporting drift for an
// absent one is true of the file and useless to the reader.
func TestWithDriftReportsOnlyDetectedHarnesses(t *testing.T) {
	states := []HarnessState{
		{Kind: HarnessClaude, Detected: true},
		{Kind: HarnessCodex, Detected: false},
		{Kind: HarnessOpenCode, Detected: true},
	}
	result := SyncResult{Drift: []DriftResult{
		{Harness: "claude", Path: "/p/CLAUDE.md", Stale: true, Reason: "missing on disk"},
		{Harness: "codex", Path: "/p/AGENTS.md", Stale: true, Reason: "missing on disk"},
		{Harness: "opencode", Path: "/p/AGENTS.md", Stale: false},
	}}
	got := WithDrift(states, result)

	if !got[0].Drift || got[0].DriftReason != "missing on disk" || got[0].InstructionPath != "/p/CLAUDE.md" {
		t.Errorf("a detected, drifted harness reported %+v", got[0])
	}
	if got[1].Drift || got[1].DriftReason != "" || got[1].InstructionPath != "" {
		t.Errorf("an ABSENT harness reported drift: %+v", got[1])
	}
	if got[2].Drift || got[2].DriftReason != "" {
		t.Errorf("a detected, in-sync harness reported drift: %+v", got[2])
	}
	if states[0].Drift {
		t.Error("WithDrift mutated its input")
	}
}

// TestWithDriftKeepsTheFirstStaleReason covers a harness with several
// generated files: it is drifted if ANY of them is, and the reason a
// reader acts on is the first stale one, not whichever came last.
func TestWithDriftKeepsTheFirstStaleReason(t *testing.T) {
	got := WithDrift([]HarnessState{{Kind: HarnessCodex, Detected: true}}, SyncResult{Drift: []DriftResult{
		{Harness: "codex", Path: "/a", Stale: true, Reason: "missing on disk"},
		{Harness: "codex", Path: "/b", Stale: false},
	}})
	if !got[0].Drift || got[0].DriftReason != "missing on disk" {
		t.Fatalf("got %+v, want the first stale reason", got[0])
	}
}
