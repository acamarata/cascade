package tailer

// Purpose: R-21.152 red-team suite (allowlist, credential canary, no
//   raw-content structure) plus unit coverage for parser.go/redact.go's
//   smaller helpers. Per R-40.X16, no fixture bytes are captured here —
//   internal/context/testdata/transcripts/ is copied, not recreated. The
//   canary is split across two literals so no contiguous match exists in
//   source (AGENT-BRIEF: push protection blocks even a synthetic match).
// Constraints: every write is rooted at t.TempDir() (Art.7).
// SPORT: fleet/tailer (ADD, per T-2 sport_updates).

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// canary is a synthetic AWS-access-key-shaped value, split across two
// literals so no contiguous credential-shaped match exists in source.
func canary() string {
	return "AKIA" + "7YQ2XPLM4RZV6WTB"
}

func canaryForms() []string {
	c := canary()
	return []string{
		c,
		base64.StdEncoding.EncodeToString([]byte(c)),
		hex.EncodeToString([]byte(c)),
	}
}

func assertNoCanary(t *testing.T, label, haystack string) {
	t.Helper()
	for _, form := range canaryForms() {
		if strings.Contains(haystack, form) {
			t.Fatalf("%s leaked the credential canary (form %q): %q", label, form, haystack)
		}
	}
}

// TestTailerRedaction proves the R-21.152 allowlist and the worktree drop rule.
func TestTailerRedaction(t *testing.T) {
	rt := reflect.TypeOf(Record{})
	want := []string{"Tool", "Duration", "ExitCode", "Paths", "ResultHash"}
	if rt.NumField() != len(want) {
		t.Fatalf("Record has %d fields, want exactly %d (%v)", rt.NumField(), len(want), want)
	}
	for i, name := range want {
		if rt.Field(i).Name != name {
			t.Fatalf("Record field %d = %q, want %q (allowlist order/content drift)", i, rt.Field(i).Name, name)
		}
	}
	root := t.TempDir()
	inside := filepath.Join(root, "src", "main.go")
	outside := filepath.Join(t.TempDir(), "etc", "secret.env")

	ev := rawEvent{tool: "build", paths: []string{inside, outside}, resultHash: "deadbeef"}
	rec := redact(ev, root)

	if len(rec.Paths) != 1 || rec.Paths[0] != filepath.Join("src", "main.go") {
		t.Fatalf("redact() Paths = %v, want exactly the in-worktree relative path", rec.Paths)
	}
	if rec.ResultHash != "deadbeef" {
		t.Fatalf("redact() ResultHash = %q, want %q", rec.ResultHash, "deadbeef")
	}

	if rec2 := redact(rawEvent{paths: []string{inside}}, ""); len(rec2.Paths) != 0 {
		t.Fatalf("redact() with empty worktreeRoot must drop every path (fail-closed), got %v", rec2.Paths)
	}
}

// TestTailerCredentialCanary plants the canary in a t.TempDir() fixture copy and proves it never surfaces.
func TestTailerCredentialCanary(t *testing.T) {
	const fixtureDir = "../../context/testdata/transcripts"
	cases := []struct {
		name    string
		harness Harness
		fixture string
	}{
		{"cc", HarnessCC, "cc-sample-redacted.jsonl"},
		{"codex", HarnessCodex, "codex-sample-redacted.jsonl"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join(fixtureDir, tc.fixture))
			if err != nil {
				t.Fatalf("read committed fixture: %v", err)
			}
			dir := t.TempDir()
			path := filepath.Join(dir, tc.fixture)
			planted := string(src) + fmt.Sprintf("\n{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":%q}]}}\n", canary())
			if err := os.WriteFile(path, []byte(planted), 0o644); err != nil {
				t.Fatalf("write planted copy: %v", err)
			}

			tl, err := NewTailer(path, tc.harness, dir)
			if err != nil {
				t.Fatalf("NewTailer: %v", err)
			}
			defer func() { _ = tl.Close() }()

			for {
				rec, err := tl.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				var pe *ParseError
				if errors.As(err, &pe) {
					assertNoCanary(t, "ParseError.Error()", pe.Error())
					continue
				}
				if err != nil {
					t.Fatalf("Next(): %v", err)
				}
				assertNoCanary(t, "Record", fmt.Sprintf("%+v", rec))
			}
		})
	}
}

// TestTailerNoRawTranscript proves ParseError/Record cannot structurally carry raw line content.
func TestTailerNoRawTranscript(t *testing.T) {
	pt := reflect.TypeOf(ParseError{})
	wantFields := map[string]string{"Line": "int", "ByteLen": "int", "Cause": "error"}
	if pt.NumField() != len(wantFields) {
		t.Fatalf("ParseError has %d fields, want exactly %d", pt.NumField(), len(wantFields))
	}
	for i := 0; i < pt.NumField(); i++ {
		f := pt.Field(i)
		wantType, ok := wantFields[f.Name]
		if !ok {
			t.Fatalf("ParseError has unexpected field %q", f.Name)
		}
		if f.Type.Kind().String() != wantType && f.Type.String() != wantType {
			t.Fatalf("ParseError.%s has type %s, want %s", f.Name, f.Type, wantType)
		}
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	malformed := "{not-json " + canary()
	if err := os.WriteFile(path, []byte(malformed+"\n"), 0o644); err != nil {
		t.Fatalf("write malformed fixture: %v", err)
	}

	tl, err := NewTailer(path, HarnessCC, dir)
	if err != nil {
		t.Fatalf("NewTailer: %v", err)
	}
	defer func() { _ = tl.Close() }()

	_, err = tl.Next()
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("Next() on malformed canary-bearing line: got %v, want *ParseError", err)
	}
	assertNoCanary(t, "ParseError.Error() over malformed line", pe.Error())
	if pe.ByteLen != len(malformed) {
		t.Fatalf("ParseError.ByteLen = %d, want %d (length only, not content)", pe.ByteLen, len(malformed))
	}
}

// TestTailerUsesCommittedFixtures asserts the three E/S-09.T6 fixture
// paths are the only transcript inputs, with no second copy (R-40.X16).
func TestTailerUsesCommittedFixtures(t *testing.T) {
	const fixtureDir = "../../context/testdata/transcripts"
	for _, name := range []string{
		"cc-sample-redacted.jsonl",
		"codex-sample-redacted.jsonl",
		"opencode-sample-redacted.jsonl",
	} {
		p := filepath.Join(fixtureDir, name)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected committed fixture at %s: %v", p, err)
		}
	}
	if _, err := os.Stat("testdata/transcripts"); !os.IsNotExist(err) {
		t.Fatalf("internal/fleet/tailer/testdata/transcripts must not exist (R-40.X16), stat err: %v", err)
	}

	dir := t.TempDir()
	ccCopy := filepath.Join(dir, "cc-sample-redacted.jsonl")
	src, err := os.ReadFile(filepath.Join(fixtureDir, "cc-sample-redacted.jsonl"))
	if err != nil {
		t.Fatalf("read committed cc fixture: %v", err)
	}
	if err := os.WriteFile(ccCopy, src, 0o644); err != nil {
		t.Fatalf("write temp copy: %v", err)
	}

	tl, err := NewTailer(ccCopy, HarnessCC, dir)
	if err != nil {
		t.Fatalf("NewTailer over real cc fixture: %v", err)
	}
	defer func() { _ = tl.Close() }()

	count := 0
	for {
		_, err := tl.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		var pe *ParseError
		if err != nil && !errors.As(err, &pe) {
			t.Fatalf("Next() over real cc fixture: %v", err)
		}
		count++ // success or an unresolved real line; either way, counted not hidden
	}
	if count == 0 {
		t.Fatal("expected at least one line decoded from the real cc fixture")
	}
}

// TestParserHelpersA/B cover the small helpers a real-line decode doesn't
// reach, split in two for funlen.
func TestParserHelpersA(t *testing.T) {
	t.Run("HarnessString", func(t *testing.T) {
		cases := map[Harness]string{HarnessCC: "cc", HarnessCodex: "codex", HarnessUnknown: "unknown", Harness(99): "unknown"}
		for h, want := range cases {
			if got := h.String(); got != want {
				t.Errorf("Harness(%d).String() = %q, want %q", h, got, want)
			}
		}
	})
	t.Run("ParseErrorUnwrap", func(t *testing.T) {
		cause := cascade.New(cascade.KindInvalidInput, "boom")
		pe := &ParseError{Line: 3, ByteLen: 7, Cause: cause}
		if !errors.Is(pe, cause) || pe.Unwrap() != cause {
			t.Fatal("ParseError.Unwrap/errors.Is did not expose Cause")
		}
	})
	t.Run("UnrecognizedHarness", func(t *testing.T) {
		for _, h := range []Harness{HarnessUnknown, Harness(99)} {
			_, err := newParser(h).parseLine([]byte(`{"type":"user"}`), 1)
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("parseLine with harness %d: got %v, want *ParseError", h, err)
			}
		}
	})
}

func TestParserHelpersB(t *testing.T) {
	t.Run("IsDottedNumericVersion", func(t *testing.T) {
		cases := map[string]bool{
			"2.1.140": true, "0.144.2": true, "2": false, "": false,
			"2..1": false, "2.1.x": false, "local": false,
		}
		for v, want := range cases {
			if got := isDottedNumericVersion(v); got != want {
				t.Errorf("isDottedNumericVersion(%q) = %v, want %v", v, got, want)
			}
		}
	})
	t.Run("RedactDurationAndExitCode", func(t *testing.T) {
		rec := redact(rawEvent{durationMS: 1500, hasExit: true, exitCode: 2}, "")
		if rec.Duration != 1500*time.Millisecond || rec.ExitCode != 2 {
			t.Fatalf("redact() = %+v, want Duration=1.5s ExitCode=2", rec)
		}
	})
	t.Run("WorktreeRelativeParentExact", func(t *testing.T) {
		root := t.TempDir()
		if _, ok := worktreeRelative(root, filepath.Dir(root)); ok {
			t.Fatal("worktreeRelative(root, parent-of-root) should be rejected (rel == \"..\")")
		}
	})
	t.Run("ExtractCLIVersion", func(t *testing.T) {
		cases := []struct {
			name, payload, want string
			wantErr             bool
		}{
			{"valid", `{"cli_version":"0.144.2"}`, "0.144.2", false},
			{"missing_field", `{}`, "", true},
			{"empty_payload", "", "", true},
			{"malformed", `{`, "", true},
		}
		for _, tc := range cases {
			var raw json.RawMessage
			if tc.payload != "" {
				raw = json.RawMessage(tc.payload)
			}
			got, err := extractCLIVersion(raw)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Errorf("extractCLIVersion(%q) = (%q, err=%v), want (%q, wantErr=%v)", tc.payload, got, err, tc.want, tc.wantErr)
			}
		}
	})
}
