//go:build postgres

// Purpose: the embedding/metadata wire-encoding helpers pgvector.go's
//
//	Upsert/Query use, split out under R-14.117 (Art.10.3's 300-line cap
//	authorizes in-package splits; this file joins pgvector.go's authorized
//	write set automatically per that ruling).
//
// Constraints: vectorLiteral formats []float32 values through
//
//	strconv.FormatFloat — never fmt.Sprintf on caller-controlled strings —
//	and the RESULT is always bound as an ordinary $N placeholder parameter
//	by the caller, never concatenated into SQL text. metadataJSON/
//	decodeMetadata round-trip provider.Vector.Metadata through
//	encoding/json, likewise bound as a placeholder. Neither function ever
//	builds a query string.
//
// SPORT: providers.pgvector.VectorStore/ADDED (P1-E17-W4-S38-T4).

package pgvector

import (
	"encoding/json"
	"strconv"
	"strings"
)

// vectorLiteral renders values in pgvector's text input format
// ("[v1,v2,...]"), which the query binds as an ordinary string parameter
// cast to ::vector server-side ($N::vector) — never spliced into the SQL
// text itself, so there is no injection surface even though pgvector has
// no native database/sql/driver.Valuer support in this pure-Go, no-CGO
// stack.
func vectorLiteral(values []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(v), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// metadataJSON marshals m to its JSON text form for binding as a $N::jsonb
// parameter. A nil map marshals to "{}" so an empty Filter matches every
// row via JSONB containment (`metadata @> '{}'::jsonb` is true for any
// object).
func metadataJSON(m map[string]any) (string, error) {
	if m == nil {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// decodeMetadata unmarshals a JSONB column's raw bytes back into a
// map[string]any for a VectorMatch's Metadata field.
func decodeMetadata(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}
