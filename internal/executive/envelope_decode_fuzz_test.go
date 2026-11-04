package executive

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// FuzzExecEnvelopeDecode is the fuzz target for DecodeEnvelope (TestEXE01LegacyAC12).
// It runs clean for the configured seed corpus and random inputs.
func FuzzExecEnvelopeDecode(f *testing.F) {
	seeds := []string{
		`{"event_id":"evt-1","kind":"exec.task.started","stream_id":"s1","stream_seq":1,"run_id":"r1","project_id":"p1","producer":"prod","payload_version":1,"payload":null,"occurred_at":"2024-01-01T00:00:00Z"}`,
		`{"event_id":"evt-1","kind":"exec.task.started","event_id":"evt-2"}`,
		`{not valid json`,
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data string) {
		env, err := DecodeEnvelope([]byte(data))
		if err != nil {
			// Error is expected for malformed or duplicate-key input.
			return
		}
		// If no error, the envelope must be internally consistent.
		if env.EventID == "" {
			t.Error("decoded envelope has empty EventID")
		}
		if _, err := ParseExecKind(string(env.Kind)); err != nil {
			t.Errorf("decoded envelope has invalid kind: %v", err)
		}
		// Verify it can round-trip through json.Marshal
		if _, err := json.Marshal(env); err != nil {
			t.Errorf("failed to marshal decoded envelope: %v", err)
		}
	})
}

// TestEXE01LegacyAC12 runs the checked-in FuzzExecEnvelopeDecode seed
// corpus through DecodeEnvelope: valid.json decodes, duplicate-key.json is
// refused as a duplicate key and malformed.json as malformed input.
func TestEXE01LegacyAC12(t *testing.T) {
	want := map[string]error{"valid.json": nil, "duplicate-key.json": errDuplicateKey, "malformed.json": errMalformed}
	for name, wantErr := range want {
		raw, err := os.ReadFile(filepath.Join("testdata", "fuzz", "FuzzExecEnvelopeDecode", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		lines := strings.SplitN(string(raw), "\n", 2)
		if len(lines) != 2 || lines[0] != "go test fuzz v1" {
			t.Fatalf("%s: not a go fuzz v1 corpus file", name)
		}
		body := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(lines[1]), "string("), ")")
		data, err := strconv.Unquote(body)
		if err != nil {
			t.Fatalf("%s: unquote: %v", name, err)
		}
		_, err = DecodeEnvelope([]byte(data))
		if !errors.Is(err, wantErr) || (wantErr == nil) != (err == nil) {
			t.Errorf("%s: DecodeEnvelope err = %v, want %v", name, err, wantErr)
		}
	}
}
