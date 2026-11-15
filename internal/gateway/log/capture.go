// Package log provides request capture and logging utilities.
package log

import (
	"bytes"
	"encoding/json"
	"io"
)

// CallRecord holds captured request data for logging.
type CallRecord struct {
	// RequestBody is the raw request body bytes.
	RequestBody []byte
}

// CanonicalRequest decodes JSON using json.Decoder with UseNumber to
// preserve large integers exactly, then re-encodes. If decoding fails
// due to non-JSON bodies or trailing data after the JSON value, it
// returns the original raw bytes unchanged without error.
func CanonicalRequest(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return raw, nil
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var val any
	if err := dec.Decode(&val); err != nil {
		return raw, nil
	}

	// Check for trailing data after the JSON value.
	if dec.More() {
		return raw, nil
	}

	// Ensure no non-whitespace trailing bytes remain.
	buf := make([]byte, 1)
	for {
		_, err := dec.Buffered().Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			return raw, nil
		}
		if buf[0] != ' ' && buf[0] != '\t' && buf[0] != '\n' && buf[0] != '\r' {
			return raw, nil
		}
	}

	out, err := json.Marshal(val)
	if err != nil {
		return raw, nil
	}
	return out, nil
}
