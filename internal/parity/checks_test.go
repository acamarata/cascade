package parity

import (
	"reflect"
	"testing"
)

func TestRequiredChecksOrder(t *testing.T) {
	want := []string{"memory", "safety", "lifecycle", "cutover-rehearsal"}
	got := RequiredChecks()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RequiredChecks() = %v, want %v", got, want)
	}
}

func TestMissingChecksAbsent(t *testing.T) {
	want := []string{"memory", "safety", "lifecycle", "cutover-rehearsal"}
	got := MissingChecks(nil)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MissingChecks(nil) = %v, want %v", got, want)
	}
}

func TestMissingChecksFailing(t *testing.T) {
	checks := []Check{
		{Name: "memory", Pass: true},
		{Name: "safety", Pass: false},
		{Name: "lifecycle", Pass: true},
		{Name: "cutover-rehearsal", Pass: true},
	}
	want := []string{"safety"}
	got := MissingChecks(checks)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MissingChecks(...) = %v, want %v", got, want)
	}
}
