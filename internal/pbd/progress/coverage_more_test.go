package progress

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendChangeRejectsBadStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "progress.jsonl")
	cases := []struct {
		name   string
		status string
	}{
		{"empty", ""},
		{"unknown", "done"},
		{"wrong case", "Ready"},
		{"padded", " ready"},
		{"underscore variant", "needs_owner"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AppendChange(path, Change{Ticket: "T:1", Status: tc.status, Attempt: 1})
			if !errors.Is(err, ErrInvalidChange) {
				t.Fatalf("status %q: expected ErrInvalidChange, got %v", tc.status, err)
			}
			if errors.Is(err, ErrAppendChange) {
				t.Errorf("status %q: must not be an append error", tc.status)
			}
		})
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file must not be created on validation failure, stat err=%v", err)
	}
}

func TestAppendChangeAcceptsEveryStatus(t *testing.T) {
	statuses := []string{
		"provisional", "ready", "running", "review", "landed",
		"parked", "blocked", "cancelled", "needs-owner",
	}
	path := filepath.Join(t.TempDir(), "progress.jsonl")
	for i, st := range statuses {
		c := Change{Ticket: "T:" + st, Status: st, Attempt: i + 1, Label: "l-" + st}
		if err := AppendChange(path, c); err != nil {
			t.Fatalf("status %q: unexpected error %v", st, err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != len(statuses) {
		t.Fatalf("expected %d lines, got %d", len(statuses), len(lines))
	}
	for i, ln := range lines {
		var got Change
		if err := json.Unmarshal([]byte(ln), &got); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		want := Change{Ticket: "T:" + statuses[i], Status: statuses[i], Attempt: i + 1, Label: "l-" + statuses[i]}
		if got != want {
			t.Errorf("line %d: expected %+v, got %+v", i, want, got)
		}
	}
}

func TestAppendChangeBoundaryAndEncoding(t *testing.T) {
	cases := []struct {
		name string
		c    Change
		want string
	}{
		{"attempt one", Change{Ticket: "A", Status: "ready", Attempt: 1}, `{"ticket":"A","status":"ready","attempt":1,"label":""}`},
		{"large attempt", Change{Ticket: "A", Status: "landed", Attempt: 1 << 30, Label: "x"}, `{"ticket":"A","status":"landed","attempt":1073741824,"label":"x"}`},
		{"escaped label", Change{Ticket: "A", Status: "parked", Attempt: 2, Label: "a\"b\n"}, `{"ticket":"A","status":"parked","attempt":2,"label":"a\"b\n"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "p.jsonl")
			if err := AppendChange(path, tc.c); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, []byte(tc.want+"\n")) {
				t.Errorf("expected %q, got %q", tc.want+"\n", data)
			}
		})
	}
}

func TestAppendChangeOpenFailures(t *testing.T) {
	dir := t.TempDir()
	c := Change{Ticket: "T:1", Status: "ready", Attempt: 1}
	cases := []struct {
		name string
		path string
	}{
		{"path is a directory", dir},
		{"missing parent", filepath.Join(dir, "missing", "deep", "p.jsonl")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AppendChange(tc.path, c)
			if !errors.Is(err, ErrAppendChange) {
				t.Fatalf("expected ErrAppendChange, got %v", err)
			}
			if errors.Is(err, ErrInvalidChange) {
				t.Errorf("must not be a validation error: %v", err)
			}
		})
	}
}

func TestAppendChangeWriteFailure(t *testing.T) {
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("/dev/full not available on this platform")
	}
	err := AppendChange("/dev/full", Change{Ticket: "T:1", Status: "ready", Attempt: 1})
	if !errors.Is(err, ErrAppendChange) {
		t.Fatalf("expected ErrAppendChange from a failing write, got %v", err)
	}
}

func TestAppendChangeKeepsExistingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.jsonl")
	if err := os.WriteFile(path, []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendChange(path, Change{Ticket: "T:1", Status: "review", Attempt: 3, Label: "r"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "seed\n" + `{"ticket":"T:1","status":"review","attempt":3,"label":"r"}` + "\n"
	if string(data) != want {
		t.Errorf("expected %q, got %q", want, data)
	}
}

func TestErrorSentinelsDistinct(t *testing.T) {
	if errors.Is(ErrInvalidChange, ErrAppendChange) || errors.Is(ErrAppendChange, ErrInvalidChange) {
		t.Error("sentinels must be distinct")
	}
	if !strings.HasPrefix(ErrInvalidChange.Error(), "D2242") || !strings.HasPrefix(ErrAppendChange.Error(), "D2289") {
		t.Errorf("unexpected sentinel codes: %v / %v", ErrInvalidChange, ErrAppendChange)
	}
}
