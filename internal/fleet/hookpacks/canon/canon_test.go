package canon

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// captureEntry is one record of testdata/real-cc-hooks/events.json.
type captureEntry struct {
	Event         string `json:"event"`
	Summary       string `json:"summary"`
	Description   string `json:"description"`
	Exit2Line     string `json:"exit2_line"`
	OtherExitLine string `json:"other_exit_line"`
}

func loadCapture(t *testing.T) []captureEntry {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "real-cc-hooks", "events.json"))
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	var entries []captureEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("decode capture: %v", err)
	}
	return entries
}

func TestHookEventCanonClosed(t *testing.T) {
	entries := loadCapture(t)
	want := make([]string, 0, len(entries))
	for _, e := range entries {
		want = append(want, e.Event)
	}
	sort.Strings(want)
	got := Names()
	if len(got) != 33 || len(want) != 33 {
		t.Fatalf("Names() has %d, capture has %d; want 33 each", len(got), len(want))
	}
	if !sort.StringsAreSorted(got) {
		t.Fatalf("Names() not sorted: %v", got)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Names() = %v\ncapture = %v", got, want)
	}
	got[0] = "mutated"
	if Names()[0] == "mutated" {
		t.Fatal("Names() returned shared storage, want a copy")
	}
	for _, n := range want {
		ev, err := Parse(n)
		if err != nil || string(ev) != n {
			t.Fatalf("Parse(%q) = %q, %v", n, ev, err)
		}
		if c := ev.ExitTwo(); c < Blocks || c > AnyNonzeroFails {
			t.Fatalf("%s has class %d outside the closed set", n, c)
		}
	}
	if Event("Bogus").ExitTwo() != 0 {
		t.Fatal("unknown event has a class")
	}
}

func TestCanonMatchesCapture(t *testing.T) {
	entries := loadCapture(t)
	seen := map[string]bool{}
	lines, blocks, feedback := 0, 0, 0
	for _, e := range entries {
		ev := Event(e.Event)
		if e.Exit2Line == "" {
			want := NoExitTwo
			if c, ok := otherExitLines[e.OtherExitLine]; ok {
				want = c
			}
			if ev == WorktreeCreate && want != AnyNonzeroFails {
				t.Errorf("WorktreeCreate other line %q not mapped to AnyNonzeroFails", e.OtherExitLine)
			}
			if got := ev.ExitTwo(); got != want {
				t.Errorf("%s: class %d, want %d (no exit-2 line)", e.Event, got, want)
			}
			continue
		}
		lines++
		class, ok := exitTwoLines[e.Exit2Line]
		if !ok {
			t.Errorf("%s: capture line %q absent from table", e.Event, e.Exit2Line)
			continue
		}
		seen[e.Exit2Line] = true
		if got := ev.ExitTwo(); got != class {
			t.Errorf("%s: constant class %d, line class %d", e.Event, got, class)
		}
		switch class {
		case Blocks:
			blocks++
		case FeedbackOnly:
			feedback++
		case NoExitTwo, AnyNonzeroFails:
			t.Errorf("%s: exit-2 line carries class %d, want a blocking or feedback class", e.Event, class)
		}
	}
	for line := range exitTwoLines {
		if !seen[line] {
			t.Errorf("table line %q absent from capture", line)
		}
	}
	if lines != 19 || blocks != 14 || feedback != 5 {
		t.Errorf("exit-2 lines %d (blocks %d, feedback %d), want 19 (14, 5)", lines, blocks, feedback)
	}
}

func TestParseRefusesVariants(t *testing.T) {
	for _, s := range []string{"stop", " Stop", "Stop ", "STOP", "", "WithheldNotice"} {
		ev, err := Parse(s)
		if err == nil || ev != "" {
			t.Fatalf("Parse(%q) = %q, %v; want refusal", s, ev, err)
		}
		if !errors.Is(err, ErrUnknownEvent) || !strings.HasPrefix(err.Error(), ErrUnknownEvent.Error()) {
			t.Fatalf("Parse(%q) error %v does not wrap ErrUnknownEvent", s, err)
		}
	}
}

func TestRefuseUsesExitTwo(t *testing.T) {
	for _, n := range Names() {
		ev := Event(n)
		cmd, err := RefusalCommand(ev, "no")
		if ev.ExitTwo() == Blocks {
			if err != nil || !strings.HasSuffix(cmd, "exit 2") {
				t.Fatalf("%s: RefusalCommand = %q, %v", n, cmd, err)
			}
			continue
		}
		if cmd != "" || !errors.Is(err, ErrEventCannotBlock) ||
			!strings.HasPrefix(err.Error(), ErrEventCannotBlock.Error()) {
			t.Fatalf("%s: RefusalCommand = %q, %v; want ErrEventCannotBlock", n, cmd, err)
		}
	}
	for _, ev := range []Event{SessionStart, PostToolUse} {
		if _, err := RefusalCommand(ev, "x"); !errors.Is(err, ErrEventCannotBlock) {
			t.Fatalf("%s must not refuse: %v", ev, err)
		}
	}
	_, err := RefusalCommand("stop", "x")
	if err == nil || errors.Is(err, ErrEventCannotBlock) || !errors.Is(err, ErrUnknownEvent) ||
		!strings.HasPrefix(err.Error(), ErrUnknownEvent.Error()) {
		t.Fatalf("unknown event error = %v, want ErrUnknownEvent", err)
	}
	if RefusalExitCode != 2 {
		t.Fatalf("RefusalExitCode = %d", RefusalExitCode)
	}
}

func TestRefusalCommandShellSafe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("RefusalCommand targets POSIX sh; /bin/sh is absent on windows")
	}
	reasons := []string{
		"it's blocked",
		"'; touch pwned; echo '",
		"$(touch pwned) `touch pwned` $HOME %s \\n",
		"multi\nline 'quoted'",
	}
	for _, reason := range reasons {
		cmd, err := RefusalCommand(PreToolUse, reason)
		if err != nil {
			t.Fatalf("RefusalCommand: %v", err)
		}
		assertShWords(t, cmd, reason)
		dir := t.TempDir()
		var stderr strings.Builder
		sh := exec.Command("/bin/sh", "-c", cmd)
		sh.Dir = dir
		sh.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir}
		sh.Stderr = &stderr
		err = sh.Run()
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			// A sandbox that forbids spawning /bin/sh leaves the static
			// sh word check above as the proof for this reason.
			t.Logf("reason %q: /bin/sh not runnable here (%v); sh word check applied", reason, err)
			continue
		}
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != RefusalExitCode {
			t.Fatalf("reason %q: run error %v, want exit 2", reason, err)
		}
		if stderr.String() != reason+"\n" {
			t.Fatalf("reason %q: stderr %q", reason, stderr.String())
		}
		if _, statErr := os.Stat(filepath.Join(dir, "pwned")); statErr == nil {
			t.Fatalf("reason %q executed injected command", reason)
		}
	}
}
