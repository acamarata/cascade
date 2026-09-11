//go:build postgres

// Purpose: unit tests for the pure encode/decode helpers (vectorLiteral,
//
//	metadataJSON, decodeMetadata) and the empty-DSN refusal — none need a
//	live server. The full VectorStore conformance run against a real
//	pgvector-enabled server is integration_test.go's job.
//
// SPORT: providers.pgvector.VectorStore/ADDED (P1-E17-W4-S38-T4).
package pgvector

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestOpen_EmptyDSN(t *testing.T) {
	_, err := Open(context.Background(), "")
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Open(\"\") = %v, want KindInvalidInput", err)
	}
}

func TestVectorLiteral(t *testing.T) {
	cases := []struct {
		values []float32
		want   string
	}{
		{nil, "[]"},
		{[]float32{1}, "[1]"},
		{[]float32{1, 0, 0}, "[1,0,0]"},
		{[]float32{0.5, -1.5}, "[0.5,-1.5]"},
	}
	for _, c := range cases {
		if got := vectorLiteral(c.values); got != c.want {
			t.Errorf("vectorLiteral(%v) = %q, want %q", c.values, got, c.want)
		}
	}
}

func TestMetadataJSON_NilMapMarshalsToEmptyObject(t *testing.T) {
	got, err := metadataJSON(nil)
	if err != nil {
		t.Fatalf("metadataJSON(nil): %v", err)
	}
	if got != "{}" {
		t.Fatalf("metadataJSON(nil) = %q, want \"{}\"", got)
	}
}

func TestMetadataJSON_RoundTrip(t *testing.T) {
	in := map[string]any{"tag": "x", "n": float64(3)}
	encoded, err := metadataJSON(in)
	if err != nil {
		t.Fatalf("metadataJSON: %v", err)
	}
	decoded, err := decodeMetadata([]byte(encoded))
	if err != nil {
		t.Fatalf("decodeMetadata: %v", err)
	}
	if decoded["tag"] != "x" || decoded["n"] != float64(3) {
		t.Fatalf("round-trip = %v, want %v", decoded, in)
	}
}

func TestDecodeMetadata_Empty(t *testing.T) {
	got, err := decodeMetadata(nil)
	if err != nil || got != nil {
		t.Fatalf("decodeMetadata(nil) = %v, %v, want nil, nil", got, err)
	}
}

func TestClassifyPgError_Default(t *testing.T) {
	if got := classifyPgError(context.DeadlineExceeded); got != cascade.KindUnavailable {
		t.Fatalf("classifyPgError(non-PgError) = %v, want KindUnavailable", got)
	}
}
