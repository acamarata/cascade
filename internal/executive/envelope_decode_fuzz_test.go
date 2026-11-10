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
		`{"event_id":"evt-1","kind":"exec.TASK_STARTED","stream_id":"s1","stream_seq":1,"run_id":"r1","project_id":"p1","producer":"prod","payload_version":1,"payload":null,"occurred_at":"2024-01-01T00:00:00Z"}`,
		`{"event_id":"evt-1","kind":"exec.TASK_STARTED","event_id":"evt-2"}`,
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

func readCorpusSeed(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fuzz", "FuzzExecEnvelopeDecode", name))
	if err != nil {
		t.Fatalf("read corpus %s: %v", name, err)
	}
	lines := strings.SplitN(strings.TrimSpace(string(raw)), "\n", 2)
	if len(lines) != 2 || lines[0] != "go test fuzz v1" {
		t.Fatalf("corpus %s is not go test fuzz v1", name)
	}
	arg := strings.TrimSuffix(strings.TrimPrefix(lines[1], "string("), ")")
	s, err := strconv.Unquote(arg)
	if err != nil {
		t.Fatalf("corpus %s: unquote: %v", name, err)
	}
	return []byte(s)
}

// TestEXE01LegacyAC12 verifies the seed corpus: valid.json decodes to
// exec.TASK_STARTED, duplicate-key and malformed input still refuse.
func TestEXE01LegacyAC12(t *testing.T) {
	env, err := DecodeEnvelope(readCorpusSeed(t, "valid.json"))
	if err != nil || env.Kind != KindTaskStarted {
		t.Fatalf("valid.json: env %+v err %v; want kind exec.TASK_STARTED", env, err)
	}
	if _, err := DecodeEnvelope(readCorpusSeed(t, "duplicate-key.json")); !errors.Is(err, errDuplicateKey) {
		t.Fatalf("duplicate-key.json: err %v, want errDuplicateKey", err)
	}
	if _, err := DecodeEnvelope([]byte(`{not valid json`)); !errors.Is(err, errMalformed) {
		t.Fatalf("malformed: err %v, want errMalformed", err)
	}
}
