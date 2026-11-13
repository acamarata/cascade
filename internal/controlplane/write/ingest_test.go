package write

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseTicketYAMLValid(t *testing.T) {
	src := "- id: t1\n  phase: build\n  title: Do thing\n  status: open\n  deps: [a, b]\n"
	rows, err := ParseTicketYAML(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []Row{
		{ID: "t1", Phase: "build", Title: "Do thing", Status: "open", Deps: []string{"a", "b"}},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("got %v, want %v", rows, want)
	}
}

func TestParseTicketYAMLMalformed(t *testing.T) {
	_, err := ParseTicketYAML("not valid yaml content")
	if err == nil {
		t.Fatal("expected error for malformed input")
	}
	if !errors.Is(err, ErrD3825) {
		t.Errorf("expected ErrD3825, got %v", err)
	}
}

func TestParseTicketYAMLEmpty(t *testing.T) {
	rows, err := ParseTicketYAML("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rows == nil {
		t.Fatal("expected non-nil empty slice")
	}
	if len(rows) != 0 {
		t.Errorf("expected empty slice, got %v", rows)
	}
}
