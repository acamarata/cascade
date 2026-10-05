package main

// Purpose: TestRegenIsReproducible proves the committed recording is exactly
//   the generator's output: two runs into separate temp directories are
//   byte-identical, and each equals testdata/recorded/real-embedder.jsonl.
//   The remaining tests pin the program's refusals and its fixed output path.
// Inputs: the committed corpus, query files and recording (read only).
// Outputs: n/a (test-only).
// Constraints: every write lands under t.TempDir(); the committed recording
//   is never written; no clock, no network.
// SPORT: placeholder: retrieval/evaluation (ADD, P1-GWY-12).

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/output"
)

const testdataDir = "../../../testdata"

func quietWriter(errOut *bytes.Buffer) *output.Writer {
	return output.New(&bytes.Buffer{}, errOut, false, false, false, true)
}

// committedDate reads the date field of the committed recording, so the test
// regenerates with the date the committed file claims.
func committedDate(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testdataDir, "recorded", "real-embedder.jsonl"))
	if err != nil {
		t.Fatalf("reading the committed recording: %v", err)
	}
	var head struct {
		Date string `json:"date"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		t.Fatalf("parsing the committed recording: %v", err)
	}
	return head.Date
}

func regenInto(t *testing.T, out string, date string) {
	t.Helper()
	var errOut bytes.Buffer
	args := []string{"-date", date, "-testdata", testdataDir, "-out", out}
	if code := realMain(args, quietWriter(&errOut)); code != 0 {
		t.Fatalf("realMain(%v) = %d: %s", args, code, errOut.String())
	}
}

func TestRegenIsReproducible(t *testing.T) {
	date := committedDate(t)
	dir := t.TempDir()
	first, second := filepath.Join(dir, "a.jsonl"), filepath.Join(dir, "b.jsonl")
	regenInto(t, first, date)
	regenInto(t, second, date)

	a, errA := os.ReadFile(first)
	b, errB := os.ReadFile(second)
	if errA != nil || errB != nil {
		t.Fatalf("reading generated files: %v, %v", errA, errB)
	}
	if len(a) == 0 {
		t.Fatal("the generator wrote an empty file")
	}
	if !bytes.Equal(a, b) {
		t.Fatal("two regenerations differ: the generator is not deterministic")
	}
	committed, err := os.ReadFile(filepath.Join(testdataDir, "recorded", "real-embedder.jsonl"))
	if err != nil {
		t.Fatalf("reading the committed recording: %v", err)
	}
	if !bytes.Equal(a, committed) {
		t.Fatal("the generator output differs from the committed recording byte for byte")
	}
}

func TestRegenDefaultOutputIsTheRecordingPath(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"corpus.jsonl", "known-item-queries.jsonl", "semantic-paraphrase-queries.jsonl"} {
		data, err := os.ReadFile(filepath.Join(testdataDir, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Fatalf("copying %s: %v", name, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "recorded"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var errOut bytes.Buffer
	if code := realMain([]string{"-date", "2026-10-04", "-testdata", root}, quietWriter(&errOut)); code != 0 {
		t.Fatalf("realMain = %d: %s", code, errOut.String())
	}
	got, err := os.ReadFile(filepath.Join(root, "recorded", "real-embedder.jsonl"))
	if err != nil || !strings.Contains(string(got), `"date": "2026-10-04"`) {
		t.Fatalf("default output not written to recorded/real-embedder.jsonl: %v", err)
	}
}

func TestRegenRefusals(t *testing.T) {
	tmp := t.TempDir()
	cases := []struct {
		name string
		args []string
		code int
		want string
	}{
		{"unknown flag", []string{"-nope"}, 2, "flag provided but not defined"},
		{"missing date", []string{"-testdata", testdataDir, "-out", filepath.Join(tmp, "x")}, 1, "-date is required"},
		{"malformed date", []string{"-date", "yesterday", "-testdata", testdataDir, "-out", filepath.Join(tmp, "x")}, 1, "YYYY-MM-DD"},
		{"missing testdata", []string{"-date", "2026-10-04", "-testdata", filepath.Join(tmp, "absent"), "-out", filepath.Join(tmp, "x")}, 1, "reading"},
		{"unwritable output", []string{"-date", "2026-10-04", "-testdata", testdataDir, "-out", filepath.Join(tmp, "no", "such", "dir", "x")}, 1, "writing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var errOut bytes.Buffer
			if code := realMain(tc.args, quietWriter(&errOut)); code != tc.code {
				t.Fatalf("exit = %d, want %d (%s)", code, tc.code, errOut.String())
			}
			if !strings.Contains(errOut.String(), tc.want) {
				t.Fatalf("stderr %q does not contain %q", errOut.String(), tc.want)
			}
		})
	}
}

func TestRegenRefusesAMalformedCorpus(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"corpus.jsonl", "known-item-queries.jsonl", "semantic-paraphrase-queries.jsonl"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("{not json\n"), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	var errOut bytes.Buffer
	args := []string{"-date", "2026-10-04", "-testdata", root, "-out", filepath.Join(root, "out")}
	if code := realMain(args, quietWriter(&errOut)); code != 1 {
		t.Fatalf("exit = %d, want 1 (%s)", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(root, "out")); err == nil {
		t.Fatal("an output file was written despite the malformed corpus")
	}
}
