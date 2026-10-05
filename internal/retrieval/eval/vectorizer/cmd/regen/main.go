// Package main regenerates internal/retrieval/eval/testdata/recorded/real-embedder.jsonl
// from the committed corpus, the two committed query files and the committed
// vocabulary. Run from the repository root:
//
//	go run ./internal/retrieval/eval/vectorizer/cmd/regen -date YYYY-MM-DD
//
// The method is specified by the doc comment of package vectorizer. The date
// is a flag, never the clock, so two runs with the same inputs write the same
// bytes. The program writes one file and prints nothing on success.
//
// Inputs: -date (required), -testdata (default internal/retrieval/eval/testdata),
// corpus.jsonl, known-item-queries.jsonl and semantic-paraphrase-queries.jsonl
// inside it, and the vocabulary compiled into package vectorizer.
// Outputs: -out (default <testdata>/recorded/real-embedder.jsonl).
// Constraints: no clock, no network, no subprocess; failures go through
// internal/output.
// SPORT: placeholder: retrieval/evaluation (ADD, P1-GWY-12).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/retrieval/eval"
	"github.com/acamarata/cascade/internal/retrieval/eval/vectorizer"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	defaultTestdata = "internal/retrieval/eval/testdata"
	generatorPath   = "internal/retrieval/eval/vectorizer/cmd/regen"
	vocabularyPath  = "internal/retrieval/eval/vectorizer/vocabulary.txt"
	recordingName   = "real-embedder.jsonl"
	recordVersion   = "1.1.0"
)

var datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func main() {
	os.Exit(realMain(os.Args[1:], output.NewDefault(false, false, false, false)))
}

// realMain is main without os.Exit: it parses args, runs the generator and
// returns the process exit code (0 ok, 1 failure, 2 bad flags).
func realMain(args []string, w *output.Writer) int {
	fs := flag.NewFlagSet("regen", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	date := fs.String("date", "", "regeneration date, YYYY-MM-DD (required)")
	testdata := fs.String("testdata", defaultTestdata, "directory holding corpus.jsonl and the two query files")
	out := fs.String("out", "", "output file (default <testdata>/recorded/"+recordingName+")")
	if err := fs.Parse(args); err != nil {
		w.Fail(cascade.Wrap(cascade.KindInvalidInput, err, "regen: bad flags"))
		return 2
	}
	if *out == "" {
		*out = filepath.Join(*testdata, "recorded", recordingName)
	}
	if err := run(*date, *testdata, *out); err != nil {
		w.Fail(err)
		return 1
	}
	return 0
}

// run renders the recording and writes it to out.
func run(date, testdata, out string) error {
	if date == "" {
		return cascade.New(cascade.KindInvalidInput, "regen: -date is required (YYYY-MM-DD)")
	}
	if !datePattern.MatchString(date) {
		return cascade.Newf(cascade.KindInvalidInput, "regen: -date %q is not YYYY-MM-DD", date)
	}
	texts, err := collectTexts(testdata)
	if err != nil {
		return err
	}
	vocab, err := vectorizer.Default()
	if err != nil {
		return err
	}
	prov := vectorizer.Provenance{
		Tool: fmt.Sprintf("%s (term-count vectorizer, vocabulary %s sha256:%s; NOT a trained embedding model)",
			generatorPath, vocabularyPath, vocab.Digest()),
		Version: recordVersion,
		Date:    date,
	}
	data, err := vectorizer.Record(vocab, prov, texts)
	if err != nil {
		return err
	}
	if err := runtime.WriteFileAtomic(out, data, 0o644); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "regen: writing %s", out)
	}
	return nil
}

// collectTexts returns every corpus document text, then every known-item
// query text, then every semantic/paraphrase query text.
func collectTexts(testdata string) ([]string, error) {
	corpus, err := openAndLoad(filepath.Join(testdata, "corpus.jsonl"), eval.LoadCorpus)
	if err != nil {
		return nil, err
	}
	var texts []string
	for _, d := range corpus.Documents {
		texts = append(texts, d.Text)
	}
	for _, name := range []string{"known-item-queries.jsonl", "semantic-paraphrase-queries.jsonl"} {
		set, err := openAndLoad(filepath.Join(testdata, name), func(r io.Reader) (eval.QuerySet, error) {
			return eval.LoadQuerySet(name, r, corpus)
		})
		if err != nil {
			return nil, err
		}
		for _, q := range set.Queries {
			texts = append(texts, q.Text)
		}
	}
	return texts, nil
}

// openAndLoad opens path and hands it to load.
func openAndLoad[T any](path string, load func(io.Reader) (T, error)) (T, error) {
	var zero T
	f, err := os.Open(path) //nolint:gosec // path is built from the -testdata flag
	if err != nil {
		return zero, cascade.Wrapf(cascade.KindUnavailable, err, "regen: reading %s", path)
	}
	defer func() { _ = f.Close() }()
	return load(f)
}
