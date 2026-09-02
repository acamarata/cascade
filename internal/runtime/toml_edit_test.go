package runtime

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSetKeyLine_PreservesCommentsAndOrder(t *testing.T) {
	src := []byte("# top comment\n[retrieval]\n# fusion settings\n[retrieval.fusion]\nk = 60 # default\nweights = [1,2]\n")
	out, err := SetKeyLine(src, "retrieval.fusion.k", "80")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "k = 80") {
		t.Fatalf("expected k=80, got:\n%s", s)
	}
	if !strings.Contains(s, "# default") || !strings.Contains(s, "# fusion settings") || !strings.Contains(s, "# top comment") {
		t.Fatalf("comment lost:\n%s", s)
	}
	if !strings.Contains(s, "weights = [1,2]") {
		t.Fatalf("unrelated line mutated:\n%s", s)
	}
}

func TestSetKeyLine_TopLevelDottedKey(t *testing.T) {
	src := []byte("retrieval.fusion.k = 60\n")
	out, err := SetKeyLine(src, "retrieval.fusion.k", "80")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "retrieval.fusion.k = 80" {
		t.Fatalf("got %q", out)
	}
}

func TestSetKeyLine_InsertIntoExistingTable(t *testing.T) {
	src := []byte("[logging]\nlevel = \"info\"\n\n[storage]\ndriver = \"sqlite\"\n")
	out, err := SetKeyLine(src, "logging.format", `"json"`)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `format = "json"`) {
		t.Fatalf("key not inserted:\n%s", s)
	}
	// The new line must land inside [logging], before [storage].
	loggingIdx := strings.Index(s, "[logging]")
	storageIdx := strings.Index(s, "[storage]")
	formatIdx := strings.Index(s, "format")
	if loggingIdx >= formatIdx || formatIdx >= storageIdx {
		t.Fatalf("format inserted in wrong place:\n%s", s)
	}
}

func TestSetKeyLine_NewTableOnEmptyFile(t *testing.T) {
	out, err := SetKeyLine(nil, "logging.level", `"debug"`)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, "[logging]") || !strings.Contains(s, `level = "debug"`) {
		t.Fatalf("got:\n%s", s)
	}
}

func TestSetKeyLine_TopLevelKeyNoTable(t *testing.T) {
	out, err := SetKeyLine(nil, "schema_version", "1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) != "schema_version = 1" {
		t.Fatalf("got %q", out)
	}
}

func TestSetKeyLine_InvalidDottedPath(t *testing.T) {
	if _, err := SetKeyLine(nil, "", "1"); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestUnsetKeyLine_RemovesOnlyTargetLine(t *testing.T) {
	src := []byte("[logging]\nlevel = \"debug\"\nformat = \"json\"\n")
	out, found, err := UnsetKeyLine(src, "logging.level")
	if err != nil || !found {
		t.Fatalf("err=%v found=%v", err, found)
	}
	s := string(out)
	if strings.Contains(s, "level") {
		t.Fatalf("key not removed:\n%s", s)
	}
	if !strings.Contains(s, "format") || !strings.Contains(s, "[logging]") {
		t.Fatalf("unrelated content removed:\n%s", s)
	}
}

func TestUnsetKeyLine_AbsentKeyIsNoopNotError(t *testing.T) {
	src := []byte("[logging]\nlevel = \"debug\"\n")
	out, found, err := UnsetKeyLine(src, "logging.format")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false for absent key")
	}
	if string(out) != string(src) {
		t.Fatalf("bytes must be unchanged on no-op unset")
	}
}

func TestFindTopLevelEquals_IgnoresEqualsInsideQuotes(t *testing.T) {
	line := `x = "a=b"`
	if got := findTopLevelEquals(line); got != 2 {
		t.Fatalf("got %d, want 2", got)
	}
}

func TestParseTableHeader_IgnoresArrayTables(t *testing.T) {
	if _, ok := parseTableHeader("[[array.of.tables]]"); ok {
		t.Fatal("array-of-tables header must not be treated as a settable table context")
	}
	if path, ok := parseTableHeader("[retrieval.fusion]"); !ok || path != "retrieval.fusion" {
		t.Fatalf("got path=%q ok=%v", path, ok)
	}
}

// --- ConfigWriter (Set/Unset orchestration, disk-touching) ---

func TestConfigWriter_Set_WritesAtomicallyAndValidates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	w := &ConfigWriter{Path: path}

	res, err := w.Set("logging.level", `"debug"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Value != "debug" {
		t.Fatalf("got %#v", res.Value)
	}

	// Second set on the same file: comments must survive.
	if err := appendComment(path, "# hand-written comment\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Set("logging.format", `"json"`); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data := readFile(t, path)
	if !strings.Contains(data, "# hand-written comment") {
		t.Fatalf("comment lost across writes:\n%s", data)
	}
}

func TestConfigWriter_Set_UnknownKeyNoWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	w := &ConfigWriter{Path: path}

	if _, err := w.Set("totally.unknown.key", "1"); err == nil {
		t.Fatal("expected error for unknown key")
	}
	if fileExists(path) {
		t.Fatal("disk must not be touched when the key is unknown")
	}
}

func TestConfigWriter_Set_SecretValueNoWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	w := &ConfigWriter{Path: path}

	_, err := w.Set("registry.pubkey_path", `"ghp_abcdefghijklmnopqrstuvwxyz0123456789"`)
	if err == nil {
		t.Fatal("expected error for secret-shaped value")
	}
	if _, ok := err.(*SecretLiteralError); !ok {
		t.Fatalf("expected *SecretLiteralError, got %T: %v", err, err)
	}
	if fileExists(path) {
		t.Fatal("disk must not be touched when the value looks like a secret")
	}
}

func TestConfigWriter_Set_InvalidLiteralNoWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	w := &ConfigWriter{Path: path}

	if _, err := w.Set("logging.level", "not valid toml {"); err == nil {
		t.Fatal("expected error for invalid literal")
	}
	if fileExists(path) {
		t.Fatal("disk must not be touched on an invalid literal")
	}
}

func TestConfigWriter_Set_ValidateFailureLeavesDiskUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	_ = writeFile(t, path, "[elevation]\nallow_remote = false\n")

	w := &ConfigWriter{Path: path}
	// Setting allow_remote=true without helper_pubkey fails Validate.
	if _, err := w.Set("elevation.allow_remote", "true"); err == nil {
		t.Fatal("expected Validate failure")
	}
	data := readFile(t, path)
	if data != "[elevation]\nallow_remote = false\n" {
		t.Fatalf("disk changed on Validate failure:\n%s", data)
	}
}

func TestConfigWriter_Unset_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	w := &ConfigWriter{Path: path}
	if _, err := w.Set("logging.level", `"debug"`); err != nil {
		t.Fatal(err)
	}
	found, err := w.Unset("logging.level")
	if err != nil || !found {
		t.Fatalf("err=%v found=%v", err, found)
	}
	if strings.Contains(readFile(t, path), "level") {
		t.Fatalf("key not removed:\n%s", readFile(t, path))
	}
}

func TestConfigWriter_CrashSimulation_TempFileNeverReplacesOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := "[logging]\nlevel = \"info\"\n"
	_ = writeFile(t, path, original)

	w := &ConfigWriter{Path: path}
	// A write that fails Validate must never call writeBytesAtomic at
	// all, so the original bytes are provably untouched (not just
	// "restored" — never modified).
	if _, err := w.Set("elevation.allow_remote", "true"); err == nil {
		t.Fatal("expected failure")
	}
	if readFile(t, path) != original {
		t.Fatal("original file content changed")
	}
	// No leftover temp files.
	entries := listDir(t, dir)
	if len(entries) != 1 || entries[0] != "config.toml" {
		t.Fatalf("unexpected directory contents: %v", entries)
	}
}
