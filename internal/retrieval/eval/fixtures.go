package eval

// Purpose: the fixture decoders — LoadCorpus, LoadQuerySet,
//   LoadRecordedEmbeddings, LoadBaseline. Each decodes the committed
//   on-disk shape and hands the result to types.go's typed constructors,
//   so a malformed byte stream and a structurally-invalid value fail
//   through the exact same taxonomy.
// Inputs: an io.Reader over the committed fixture file.
// Outputs: the validated typed value, or a *cascade.Error.
// Constraints: JSONL decode failures, duplicate ids, missing expected
//   ids, dimension mismatches, non-finite vectors, empty sets, and
//   missing provenance all return typed errors (06-FORGE-SPEC.md §5
//   rule 1); nothing here touches the filesystem directly, so this file
//   itself needs no t.TempDir() discipline.
// SPORT: placeholder: retrieval/evaluation (ADD, P1-E06-W2-S12-T6).

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"

	"github.com/acamarata/cascade/pkg/cascade"
)

// corpusRow is one line of the committed corpus.jsonl.
type corpusRow struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// LoadCorpus decodes a JSONL corpus fixture: one corpusRow per line.
func LoadCorpus(r io.Reader) (EvalCorpus, error) {
	var docs []Document
	err := decodeJSONL(r, func(line []byte) error {
		var row corpusRow
		if jsonErr := json.Unmarshal(line, &row); jsonErr != nil {
			return cascade.Wrap(cascade.KindInvalidInput, jsonErr, "eval: decoding corpus line")
		}
		docs = append(docs, Document{ID: row.ID, Text: row.Text})
		return nil
	})
	if err != nil {
		return EvalCorpus{}, err
	}
	return NewEvalCorpus(docs)
}

// queryRow is one line of a committed known-item or semantic/paraphrase
// query-set fixture.
type queryRow struct {
	Query       string   `json:"query"`
	ExpectedIDs []string `json:"expected_ids"`
}

// LoadQuerySet decodes a JSONL query-set fixture under name, validating
// every expected id against corpus.
func LoadQuerySet(name string, r io.Reader, corpus EvalCorpus) (QuerySet, error) {
	var queries []Query
	err := decodeJSONL(r, func(line []byte) error {
		var row queryRow
		if jsonErr := json.Unmarshal(line, &row); jsonErr != nil {
			return cascade.Wrapf(cascade.KindInvalidInput, jsonErr, "eval: decoding query set %q line", name)
		}
		queries = append(queries, Query{Text: row.Query, ExpectedIDs: row.ExpectedIDs})
		return nil
	})
	if err != nil {
		return QuerySet{}, err
	}
	return NewQuerySet(name, queries, corpus)
}

// embeddingRow is one recorded vector inside a real-embedder.jsonl-style
// document.
type embeddingRow struct {
	Text   string    `json:"text"`
	Vector []float32 `json:"vector"`
}

// embeddingFile is the whole recorded-embedder fixture: one JSON document
// carrying the Art.2 provenance fields alongside every recorded vector.
type embeddingFile struct {
	Tool    string         `json:"tool"`
	Version string         `json:"version"`
	Date    string         `json:"date"`
	Records []embeddingRow `json:"records"`
}

// LoadRecordedEmbeddings decodes the recorded real-embedder fixture: a
// single JSON document, never JSONL, because the provenance fields sit
// alongside the whole record set rather than repeating on every line.
func LoadRecordedEmbeddings(r io.Reader) (RecordedEmbeddings, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return RecordedEmbeddings{}, cascade.Wrap(cascade.KindInvalidInput, err, "eval: reading recorded embeddings")
	}
	var file embeddingFile
	if err := json.Unmarshal(data, &file); err != nil {
		return RecordedEmbeddings{}, cascade.Wrap(cascade.KindInvalidInput, err, "eval: decoding recorded embeddings")
	}
	records := make([]EmbeddingRecord, len(file.Records))
	for i, row := range file.Records {
		records[i] = EmbeddingRecord{Text: row.Text, Vector: row.Vector}
	}
	return NewRecordedEmbeddings(Provenance{Tool: file.Tool, Version: file.Version, Date: file.Date}, records)
}

// baselineFile is the v1 August-2026 regression fixture's on-disk shape.
// See Baseline's doc comment for why these are v1's own MRR@10 fields
// rather than this ticket's recall@10 shape.
type baselineFile struct {
	Tool     string  `json:"tool"`
	Version  string  `json:"version"`
	Date     string  `json:"date"`
	Metric   string  `json:"metric"`
	FTS5Only float64 `json:"fts5_only"`
	RRFFull  float64 `json:"rrf_full"`
}

// LoadBaseline decodes the harvested v1 August-2026 regression fixture.
// A zero FTS5Only is refused: FusionToFTS5Ratio would divide by it.
func LoadBaseline(r io.Reader) (Baseline, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return Baseline{}, cascade.Wrap(cascade.KindInvalidInput, err, "eval: reading baseline")
	}
	var file baselineFile
	if err := json.Unmarshal(data, &file); err != nil {
		return Baseline{}, cascade.Wrap(cascade.KindInvalidInput, err, "eval: decoding baseline")
	}
	prov := Provenance{Tool: file.Tool, Version: file.Version, Date: file.Date}
	if !prov.Valid() {
		return Baseline{}, cascade.New(cascade.KindIntegrity, "eval: baseline is missing a provenance field (tool/version/date)")
	}
	if file.Metric == "" {
		return Baseline{}, cascade.New(cascade.KindInvalidInput, "eval: baseline is missing its metric name")
	}
	if file.FTS5Only == 0 {
		return Baseline{}, cascade.New(cascade.KindInvalidInput, "eval: baseline fts5_only is zero")
	}
	return Baseline{Provenance: prov, Metric: file.Metric, FTS5Only: file.FTS5Only, RRFFull: file.RRFFull}, nil
}

// decodeJSONL calls fn once per non-empty line of r, in order, stopping
// at the first error fn or the scan itself returns.
func decodeJSONL(r io.Reader, fn func(line []byte) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := fn(line); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "eval: scanning JSONL fixture")
	}
	return nil
}
