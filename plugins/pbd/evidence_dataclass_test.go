package pbd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestPBDEvidenceDataClass proves every table and JSON row carries its class.
func TestPBDEvidenceDataClass(t *testing.T) {
	installEvidenceTestReader(t, evidenceTestReader{rows: evidenceTestRows()})
	var out, stderr bytes.Buffer
	if err := RunEvidence(context.Background(), []string{"T-1"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[1], "local_only") || !strings.Contains(lines[2], "public") {
		t.Fatalf("data classes missing: %q", out.String())
	}
	out.Reset()
	if err := RunEvidence(context.Background(), []string{"T-1", "--json"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["data_class"] != "local_only" || rows[1]["data_class"] != "public" {
		t.Fatalf("JSON classes = %v", rows)
	}
}

// TestPBDEvidenceRedaction proves snapshot and watch output honor row redaction.
func TestPBDEvidenceRedaction(t *testing.T) {
	rows := evidenceTestRows()
	rows[0].Redacted, rows[0].ArtifactRef = true, "hidden-artifact"
	for _, asJSON := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		installEvidenceTestReader(t, evidenceTestReader{rows: rows, emitted: rows, cancel: cancel})
		args := []string{"T-1", "--watch"}
		if asJSON {
			args = append(args, "--json")
		}
		var out, stderr bytes.Buffer
		if err := RunEvidence(ctx, args, &out, &stderr); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "hidden-artifact") || strings.Count(out.String(), "artifact-1") != 2 {
			t.Fatalf("artifact rendering = %q", out.String())
		}
		if asJSON {
			assertRedactedEvidenceJSON(t, out.String())
		} else if strings.Count(out.String(), "redacted: true") != 2 {
			t.Fatalf("redacted markers = %q", out.String())
		}
		if rows[0].ArtifactRef != "hidden-artifact" {
			t.Fatal("renderer mutated reader rows")
		}
	}
}

func assertRedactedEvidenceJSON(t *testing.T, output string) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var initial []map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &initial); err != nil {
		t.Fatal(err)
	}
	objects := append([]map[string]any{}, initial...)
	for _, line := range lines[1:] {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		objects = append(objects, row)
	}
	if len(objects) != 4 {
		t.Fatalf("objects = %v", objects)
	}
	for _, row := range objects {
		artifact, exists := row["artifact_ref"]
		if row["redacted"] == true && exists {
			t.Errorf("redacted artifact key: %v", row)
		}
		if row["redacted"] == false && artifact != "artifact-1" {
			t.Errorf("visible artifact missing: %v", row)
		}
	}
}
