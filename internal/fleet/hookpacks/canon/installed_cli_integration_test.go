//go:build integration

package canon

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// installedCLIGate is the skip message used when no claude binary is on
// PATH; the gate never fakes a pass.
const installedCLIGate = "integration gate: claude CLI not installed; TestCanonMatchesInstalledCLI skipped"

var (
	mapStart       = []byte("{PreToolUse:{summary:")
	eventKeyRE     = regexp.MustCompile(`[{,]([A-Z][A-Za-z]+):\{summary:`)
	captureVerRE   = regexp.MustCompile("Version: ([0-9][0-9.]*)")
	installedVerRE = regexp.MustCompile(`^([0-9][0-9.]*) `)
)

func TestCanonMatchesInstalledCLI(t *testing.T) {
	path, err := exec.LookPath("claude")
	if err != nil {
		t.Skip(installedCLIGate)
	}
	if resolved, rerr := filepath.EvalSymlinks(path); rerr == nil {
		path = resolved
	}
	checkInstalledVersion(t, path)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read installed binary: %v", err)
	}
	obj := hookMapObject(t, raw)
	events, lines := scanHookMap(obj)
	if len(events) != 33 {
		t.Fatalf("escalate: installed map has %d events, want 33", len(events))
	}
	if len(lines) != 19 {
		t.Fatalf("escalate: installed map has %d exit-2 lines, want 19", len(lines))
	}
	capture := loadCapture(t)
	var wantEvents []string
	wantLines := map[string]string{}
	for _, e := range capture {
		wantEvents = append(wantEvents, e.Event)
		if e.Exit2Line != "" {
			wantLines[e.Event] = e.Exit2Line
		}
	}
	gotEvents := append([]string(nil), events...)
	sort.Strings(gotEvents)
	sort.Strings(wantEvents)
	if strings.Join(gotEvents, ",") != strings.Join(wantEvents, ",") {
		t.Fatalf("escalate: installed events %v\ncapture %v", gotEvents, wantEvents)
	}
	for ev, line := range lines {
		if wantLines[ev] != line {
			t.Errorf("escalate: %s exit-2 line %q, capture %q", ev, line, wantLines[ev])
		}
	}
}

// checkInstalledVersion fails (escalates) when the installed CLI's
// version differs from the capture's.
func checkInstalledVersion(t *testing.T, path string) {
	t.Helper()
	readme, err := os.ReadFile(filepath.Join("testdata", "real-cc-hooks", "README.md"))
	if err != nil {
		t.Fatalf("read capture README: %v", err)
	}
	m := captureVerRE.FindSubmatch(readme)
	if m == nil {
		t.Fatal("capture README names no version")
	}
	home := t.TempDir()
	cmd := exec.Command(path, "--version")
	cmd.Dir = home
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("escalate: %s --version: %v", filepath.Base(path), err)
	}
	got := installedVerRE.FindSubmatch(bytes.TrimSpace(out))
	if got == nil || string(got[1]) != string(m[1]) {
		t.Fatalf("escalate: installed version %q, capture version %s", strings.TrimSpace(string(out)), m[1])
	}
}

// hookMapObject returns the single object literal starting with
// mapStart, up to its balanced closing brace.
func hookMapObject(t *testing.T, raw []byte) []byte {
	t.Helper()
	if n := bytes.Count(raw, mapStart); n != 1 {
		t.Fatalf("escalate: %q occurs %d times, want exactly 1", mapStart, n)
	}
	start := bytes.Index(raw, mapStart)
	end := balancedEnd(raw, start)
	if end < 0 {
		t.Fatal("escalate: hook map object has no balanced closing brace")
	}
	return raw[start : end+1]
}

// balancedEnd returns the index of the brace closing the one at start,
// skipping braces inside quote, apostrophe and backtick literals.
func balancedEnd(raw []byte, start int) int {
	depth := 0
	var quote byte
	for i := start; i < len(raw); i++ {
		c := raw[i]
		if quote != 0 {
			switch c {
			case '\\':
				i++
			case quote:
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'', '`':
			quote = c
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// scanHookMap returns the event keys in source order and each event's
// exit-2 line (text after "Exit code 2 -" up to the newline).
func scanHookMap(obj []byte) ([]string, map[string]string) {
	locs := eventKeyRE.FindAllSubmatchIndex(obj, -1)
	events := make([]string, 0, len(locs))
	lines := map[string]string{}
	for i, loc := range locs {
		name := string(obj[loc[2]:loc[3]])
		events = append(events, name)
		end := len(obj)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		if line, ok := exitTwoLine(obj[loc[1]:end]); ok {
			lines[name] = line
		}
	}
	return events, lines
}

// exitTwoLine extracts the trimmed text after "Exit code 2 -" up to an
// escaped or literal newline or the end of the string literal.
func exitTwoLine(seg []byte) (string, bool) {
	const marker = "Exit code 2 -"
	idx := bytes.Index(seg, []byte(marker))
	if idx < 0 {
		return "", false
	}
	rest := seg[idx+len(marker):]
	cut := len(rest)
	for _, stop := range [][]byte{[]byte(`\n`), []byte("\n"), []byte(`"`), []byte("`")} {
		if j := bytes.Index(rest, stop); j >= 0 && j < cut {
			cut = j
		}
	}
	return strings.TrimSpace(string(rest[:cut])), true
}
