package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: tests for toml_edit_scanner.go's line-classification helpers
//   (findTopLevelEquals, findValueSpan) and the atomic-write primitive
//   (writeBytesAtomic/WriteBytesAtomic/dirOf), split out as its own file
//   alongside toml_edit_test.go per R-14.117/Art.10.3 (300-line file
//   cap) rather than growing that file further. Added by this ticket's
//   R-14 CR fix (P1-E03-W1-S05-T8, blocking fixes 2 and 5).
// Inputs: n/a (test-only).
// Outputs: n/a (test-only).
// Constraints: Art.7.1 — every test uses t.TempDir(), no real $HOME.
// SPORT: runtime/toml-edit-engine (ADD, placeholder per T-8 sport_updates).

// TestWriteBytesAtomic_TempFileSameDirectoryAsTarget is blocking fix 2's
// required test: the crash-safe write's temp file must land in the same
// directory as the target path, on every OS — the R-14 CR defect was
// dirOf hardcoding '/' and returning "" for any path built with
// filepath.Join on Windows, sending the temp file to os.TempDir()
// instead and turning the final rename into a (non-atomic, sometimes
// refused) cross-volume move.
func TestWriteBytesAtomic_TempFileSameDirectoryAsTarget(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "nested", "config-dir")
	target := filepath.Join(sub, "config.toml")

	if err := WriteBytesAtomic(target, []byte("[runtime]\nprofile = \"local\"\n")); err != nil {
		t.Fatalf("WriteBytesAtomic: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(got) != "[runtime]\nprofile = \"local\"\n" {
		t.Fatalf("unexpected content: %q", got)
	}

	// dirOf itself must equal filepath.Dir, matching what writeBytesAtomic
	// actually passed to os.CreateTemp — proving the temp file was
	// created in-tree, not in a fallback location.
	if got, want := dirOf(target), filepath.Dir(target); got != want {
		t.Fatalf("dirOf(%q) = %q, want %q (filepath.Dir)", target, got, want)
	}
	if got, want := dirOf(target), sub; got != want {
		t.Fatalf("dirOf(%q) = %q, want the target's own directory %q", target, got, want)
	}

	// No leftover temp file: exactly the target, nothing else, in sub.
	entries, err := os.ReadDir(sub)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.toml" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("directory contents = %v, want exactly [config.toml]", names)
	}
}

// TestFindTopLevelEquals_TrailingDoubleBackslashBeforeQuote is nit 5: the
// escape tracker used to check only one preceding byte for a backslash
// rather than the full backslash run's parity, so a value ending in an
// even number of backslashes immediately before the closing quote (a
// plausible Windows path, e.g. "C:\\") was misread as still inside the
// string — the closing quote looked "escaped" when it is not (\\ is one
// escaped backslash, not an escaped quote).
func TestFindTopLevelEquals_TrailingDoubleBackslashBeforeQuote(t *testing.T) {
	// key = "C:\\" # trailing comment
	line := `key = "C:\\" # trailing comment`
	eq := findTopLevelEquals(line)
	if eq == -1 {
		t.Fatalf("findTopLevelEquals(%q) = -1, want the index of the real '='", line)
	}
	if line[:eq] != "key " {
		t.Fatalf("findTopLevelEquals matched the wrong '=': keyPart = %q", line[:eq])
	}

	rest := line[eq+1:]
	_, commentStart := findValueSpan(rest)
	if commentStart == -1 {
		t.Fatalf("findValueSpan(%q) found no comment; want the '#' after the closed string", rest)
	}
	if !strings.Contains(rest[:commentStart], `"C:\\"`) {
		t.Fatalf("value span excludes the string value: %q", rest[:commentStart])
	}
}

// TestFindTopLevelEquals_OddBackslashRunKeepsQuoteEscaped is the
// companion odd-run case: three backslashes before a quote means the
// quote genuinely IS escaped (odd number of backslashes), so the string
// is not yet closed and an '=' inside it must not be matched.
func TestFindTopLevelEquals_OddBackslashRunKeepsQuoteEscaped(t *testing.T) {
	// A string containing an escaped backslash followed by an escaped
	// quote: "a\\\"" is the four-char content a\" — still inside the
	// string right up to the real closing quote.
	line := `key = "a\\\" = not-an-equals" # comment`
	eq := findTopLevelEquals(line)
	if eq == -1 {
		t.Fatalf("findTopLevelEquals(%q) = -1, want the index of the real '='", line)
	}
	if line[:eq] != "key " {
		t.Fatalf("findTopLevelEquals matched an '=' inside the string: keyPart = %q", line[:eq])
	}
}

// TestJoinLines_TrailingNewlineException is nit 6: splitLines/joinLines
// always append a trailing newline, even when the source had none. This
// test pins that as a DOCUMENTED, deliberate exception to the
// docs/config-reference.md "byte-for-byte" round-trip claim (which this
// ticket's fix narrows to say so explicitly) rather than a silent
// surprise — a `config set` against a config.toml with no final newline
// gains exactly one trailing '\n' and nothing else changes.
func TestJoinLines_TrailingNewlineException(t *testing.T) {
	src := []byte("[runtime]\nprofile = \"local\"") // no trailing newline
	out, err := SetKeyLine(src, "runtime.profile", `"remote"`)
	if err != nil {
		t.Fatal(err)
	}
	want := "[runtime]\nprofile = \"remote\"\n"
	if string(out) != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}
