package record

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadsCountsAndNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gatewayproxy.jsonl")
	err := os.WriteFile(path, []byte("{\"name\":\"a\",\"count\":3}\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	records, err := ReadServiceRecord(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Name != "a" || records[0].Count != 3 {
		t.Errorf("expected Record{a, 3}, got %+v", records[0])
	}
}

func TestDropsOtherFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gatewayproxy.jsonl")
	err := os.WriteFile(path, []byte("{\"name\":\"a\",\"count\":3,\"text\":\"secret\"}\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	records, err := ReadServiceRecord(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Name != "a" || records[0].Count != 3 {
		t.Errorf("expected Record{a, 3}, got %+v", records[0])
	}

	// Verify that the struct doesn't have a "text" field
	val := reflect.ValueOf(records[0])
	if val.Kind() == reflect.Pointer {
		val = val.Elem()
	}
	for i := 0; i < val.NumField(); i++ {
		if strings.EqualFold(val.Type().Field(i).Name, "text") {
			t.Errorf("Record struct should not have a text field")
		}
	}
}

func TestMissingFileEmpty(t *testing.T) {
	dir := t.TempDir()
	records, err := ReadServiceRecord(dir)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("expected empty slice, got %d records", len(records))
	}
}

func TestMalformedLineErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gatewayproxy.jsonl")
	content := "{\"name\":\"a\",\"count\":3}\nnot json\n"
	err := os.WriteFile(path, []byte(content), 0644)
	if err != nil {
		t.Fatal(err)
	}

	_, err = ReadServiceRecord(dir)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("expected error to contain 'line 2', got: %v", err)
	}
}

func TestReadsCorrectPath(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	// Create a valid file in dir2 (sibling dir)
	path2 := filepath.Join(dir2, "gatewayproxy.jsonl")
	err := os.WriteFile(path2, []byte("{\"name\":\"sibling\",\"count\":99}\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// dir1 has gatewayproxy.jsonl
	path1 := filepath.Join(dir1, "gatewayproxy.jsonl")
	err = os.WriteFile(path1, []byte("{\"name\":\"correct\",\"count\":1}\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	records, err := ReadServiceRecord(dir1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Name != "correct" {
		t.Errorf("expected name 'correct', got '%s'", records[0].Name)
	}
}
