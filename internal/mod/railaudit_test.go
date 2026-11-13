package mod

import (
	"reflect"
	"testing"
)

func TestRailAuditClean(t *testing.T) {
	files := map[string]string{
		"a.go": "package a\n",
		"b.go": "package b\n",
	}
	got := FindRailCallers(files, "legacy/")
	if len(got) != 0 {
		t.Errorf("FindRailCallers() = %v; want empty", got)
	}
}

func TestRailAuditFlagsCaller(t *testing.T) {
	files := map[string]string{
		"cmd/x.go": "package main\n// cam rail ls\n",
	}
	want := []string{"cmd/x.go"}
	got := FindRailCallers(files, "")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FindRailCallers() = %v; want %v", got, want)
	}
}

func TestRailAuditIgnoresLegacy(t *testing.T) {
	files := map[string]string{
		"Cascade.app/a.go": "package main\n// cam rail\n",
	}
	got := FindRailCallers(files, "Cascade.app/")
	if len(got) != 0 {
		t.Errorf("FindRailCallers() = %v; want empty", got)
	}
}

func TestRailAuditSorted(t *testing.T) {
	files := map[string]string{
		"z.go": "cam rail",
		"a.go": "cam rail",
	}
	want := []string{"a.go", "z.go"}
	got := FindRailCallers(files, "")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FindRailCallers() = %v; want %v", got, want)
	}
}
