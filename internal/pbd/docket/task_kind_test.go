package docket

import (
	"strings"
	"testing"
)

func TestAllNineKindsAccepted(t *testing.T) {
	kinds := []TaskKind{
		KindAgent,
		KindModel,
		KindService,
		KindCI,
		KindCam,
		KindBuild,
		KindNode,
		KindScript,
		KindShell,
	}
	for _, k := range kinds {
		row := TaskRow{
			Kind:    k,
			Run:     "run-1",
			Account: "acc-1",
		}
		_, err := ValidateTaskRow(row, nil)
		if err != nil {
			t.Errorf("ValidateTaskRow(%q) returned error: %v", k, err)
		}
	}
}

func TestUnknownKindRejected(t *testing.T) {
	row := TaskRow{
		Kind:    TaskKind("cron"),
		Run:     "run-1",
		Account: "acc-1",
	}
	_, err := ValidateTaskRow(row, nil)
	if err == nil {
		t.Fatal("expected error for unknown kind, got nil")
	}
	if !strings.Contains(err.Error(), "cron") {
		t.Errorf("error %q does not name the kind 'cron'", err.Error())
	}
}

func TestMissingRunRejected(t *testing.T) {
	row := TaskRow{
		Kind:    KindAgent,
		Run:     "",
		Account: "acc-1",
	}
	_, err := ValidateTaskRow(row, nil)
	if err == nil {
		t.Fatal("expected error for missing run, got nil")
	}
	if !strings.Contains(err.Error(), "run") {
		t.Errorf("error %q does not name 'run'", err.Error())
	}
}

func TestMissingAccountRejected(t *testing.T) {
	row := TaskRow{
		Kind:    KindCI,
		Run:     "run-1",
		Account: "",
	}
	_, err := ValidateTaskRow(row, nil)
	if err == nil {
		t.Fatal("expected error for missing account, got nil")
	}
	if !strings.Contains(err.Error(), "account") {
		t.Errorf("error %q does not name 'account'", err.Error())
	}
}

func TestHeadlessInheritedFromParent(t *testing.T) {
	trueVal := true
	parent := &TaskRow{
		Kind:     KindAgent,
		Run:      "run-0",
		Account:  "acc-0",
		Headless: &trueVal,
	}
	row := TaskRow{
		Kind:    KindAgent,
		Run:     "run-1",
		Account: "acc-1",
	}
	out, err := ValidateTaskRow(row, parent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Headless == nil {
		t.Fatal("expected Headless to be inherited, got nil")
	}
	if !*out.Headless {
		t.Error("expected Headless to be true, got false")
	}
}
