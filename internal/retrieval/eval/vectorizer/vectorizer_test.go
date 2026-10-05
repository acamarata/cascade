package vectorizer_test

// Purpose: TestVectorizerSpec pins the vectorizer's documented method: the
//   vocabulary loader's refusals (each asserted by Kind AND exact message),
//   the tokenizer rule, the per-dimension count plus smoothing, and that the
//   committed vocabulary has exactly as many terms as the committed recording
//   has dimensions. The Record tests pin the on-disk shape the eval loader
//   reads.
// Inputs: the committed vocabulary and recording (read only).
// Outputs: n/a (test-only).
// Constraints: no clock, no network; nothing is written.
// SPORT: placeholder: retrieval/evaluation (ADD, P1-GWY-12).

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/eval"
	"github.com/acamarata/cascade/internal/retrieval/eval/vectorizer"
	"github.com/acamarata/cascade/pkg/cascade"
)

// wantInvalid asserts err is a taxonomy error of KindInvalidInput carrying
// exactly msg. errors.Is alone compares Kind only, so the message is checked
// by identity of the concrete *cascade.Error.
func wantInvalid(t *testing.T, err error, msg string) {
	t.Helper()
	var ce *cascade.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a *cascade.Error", err)
	}
	if ce.Kind != cascade.KindInvalidInput {
		t.Fatalf("kind = %v, want %v", ce.Kind, cascade.KindInvalidInput)
	}
	if ce.Msg != msg {
		t.Fatalf("message = %q, want %q", ce.Msg, msg)
	}
}

func mustLoad(t *testing.T, src string) vectorizer.Vocabulary {
	t.Helper()
	v, err := vectorizer.Load(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return v
}

func TestVectorizerSpec(t *testing.T) {
	loadRefusalCases(t)
	t.Run("vectorize returns the documented counts", func(t *testing.T) {
		v := mustLoad(t, "# comment\nalpha\n\nbeta\nsnake_case\n42\ngamma\n")
		got := v.Vectorize("Alpha, ALPHA; beta! snake_case snake case 42-42-42 alpha_x")
		want := []float32{2.01, 1.01, 1.01, 3.01, 0.01}
		assertVector(t, got, want)
	})
	t.Run("vectorize of empty text is all smoothing", func(t *testing.T) {
		v := mustLoad(t, "alpha\nbeta\n")
		assertVector(t, v.Vectorize(""), []float32{0.01, 0.01})
		assertVector(t, v.Vectorize("--- ..."), []float32{0.01, 0.01})
	})
	t.Run("tokenizer lower-cases", func(t *testing.T) {
		v := mustLoad(t, "straße\nalpha\n")
		assertVector(t, v.Vectorize("ALPHA Alpha STRASSE STRAßE"), []float32{1.01, 2.01})
	})
	t.Run("committed vocabulary width equals the recording width", func(t *testing.T) {
		def, err := vectorizer.Default()
		if err != nil {
			t.Fatalf("Default: %v", err)
		}
		width := len(def.Vectorize(""))
		for i, r := range readRecording(t).Records {
			if len(r.Vector) != width {
				t.Fatalf("record %d has %d dimensions, vocabulary has %d terms", i, len(r.Vector), width)
			}
		}
	})
}

// loadRefusalCases runs the Load refusal subtests of TestVectorizerSpec.
func loadRefusalCases(t *testing.T) {
	t.Helper()
	t.Run("load refuses an empty vocabulary", func(t *testing.T) {
		_, err := vectorizer.Load(strings.NewReader("# only a comment\n\n"))
		wantInvalid(t, err, "vectorizer: vocabulary has no terms")
		_, err = vectorizer.Load(strings.NewReader(""))
		wantInvalid(t, err, "vectorizer: vocabulary has no terms")
	})
	t.Run("load refuses a duplicate term", func(t *testing.T) {
		_, err := vectorizer.Load(strings.NewReader("alpha\nbeta\nalpha\n"))
		wantInvalid(t, err, `vectorizer: vocabulary line 3 repeats term "alpha" of dimension 0`)
	})
	t.Run("load refuses a term that can never match a token", func(t *testing.T) {
		_, err := vectorizer.Load(strings.NewReader("alpha\nBeta\n"))
		wantInvalid(t, err, `vectorizer: vocabulary line 2: term "Beta" is not a lower-case run of letters, digits and underscores`)
		_, err = vectorizer.Load(strings.NewReader("two words\n"))
		wantInvalid(t, err, `vectorizer: vocabulary line 1: term "two words" is not a lower-case run of letters, digits and underscores`)
	})
	t.Run("load surfaces a reader failure as invalid input", func(t *testing.T) {
		_, err := vectorizer.Load(failingReader{})
		var ce *cascade.Error
		if !errors.As(err, &ce) || ce.Kind != cascade.KindInvalidInput || ce.Msg != "vectorizer: reading vocabulary" {
			t.Fatalf("err = %v, want KindInvalidInput %q", err, "vectorizer: reading vocabulary")
		}
	})
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read refused") }

func assertVector(t *testing.T, got, want []float32) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dimension %d = %v, want %v (full %v)", i, got[i], want[i], got)
		}
	}
}

type recordingDoc struct {
	Tool    string `json:"tool"`
	Version string `json:"version"`
	Date    string `json:"date"`
	Records []struct {
		Text   string    `json:"text"`
		Vector []float32 `json:"vector"`
	} `json:"records"`
}

func readRecording(t *testing.T) recordingDoc {
	t.Helper()
	data, err := os.ReadFile("../testdata/recorded/real-embedder.jsonl")
	if err != nil {
		t.Fatalf("reading the committed recording: %v", err)
	}
	var doc recordingDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing the committed recording: %v", err)
	}
	return doc
}

func TestDefaultDigestMatchesTheCommittedFile(t *testing.T) {
	def, err := vectorizer.Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	raw, err := os.ReadFile("vocabulary.txt")
	if err != nil {
		t.Fatalf("reading vocabulary.txt: %v", err)
	}
	again, err := vectorizer.Load(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if def.Digest() != again.Digest() || len(def.Digest()) != 64 {
		t.Fatalf("digests differ or are not sha256 hex: %q vs %q", def.Digest(), again.Digest())
	}
}

func TestRecordShape(t *testing.T) {
	v := mustLoad(t, "alpha\nbeta\n")
	prov := vectorizer.Provenance{Tool: "t", Version: "1", Date: "2026-10-04"}
	got, err := vectorizer.Record(v, prov, []string{"alpha \u2014 \"q\"\n<b>", "beta", "alpha \u2014 \"q\"\n<b>", "\U0001F600 \x7f"})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	want := "{\n  \"tool\": \"t\",\n  \"version\": \"1\",\n  \"date\": \"2026-10-04\",\n  \"records\": [\n" +
		"    {\n      \"text\": \"alpha \\u2014 \\\"q\\\"\\n<b>\",\n      \"vector\": [\n        1.01,\n        0.01\n      ]\n    },\n" +
		"    {\n      \"text\": \"beta\",\n      \"vector\": [\n        0.01,\n        1.01\n      ]\n    },\n" +
		"    {\n      \"text\": \"\\ud83d\\ude00 \\u007f\",\n      \"vector\": [\n        0.01,\n        0.01\n      ]\n    }\n" +
		"  ]\n}\n"
	if string(got) != want {
		t.Fatalf("Record bytes differ:\n got %q\nwant %q", got, want)
	}
	loaded, err := eval.LoadRecordedEmbeddings(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("eval.LoadRecordedEmbeddings rejects the output: %v", err)
	}
	if loaded.Provenance.Tool != "t" || len(loaded.Records) != 3 {
		t.Fatalf("loaded provenance/records unexpected: %+v", loaded.Provenance)
	}
}

func TestRecordRefusals(t *testing.T) {
	v := mustLoad(t, "alpha\n")
	ok := vectorizer.Provenance{Tool: "t", Version: "1", Date: "2026-10-04"}
	_, err := vectorizer.Record(v, ok, nil)
	wantInvalid(t, err, "vectorizer: no texts to record")
	_, err = vectorizer.Record(v, ok, []string{"alpha", ""})
	wantInvalid(t, err, "vectorizer: text 1 is empty")
	for _, p := range []vectorizer.Provenance{{Version: "1", Date: "d"}, {Tool: "t", Date: "d"}, {Tool: "t", Version: "1"}} {
		_, err = vectorizer.Record(v, p, []string{"alpha"})
		wantInvalid(t, err, "vectorizer: provenance needs tool, version and date")
	}
}
