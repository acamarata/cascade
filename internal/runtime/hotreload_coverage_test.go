package runtime

import (
	"context"
	"testing"
	"time"
)

// Purpose: small targeted coverage top-ups for trivial branches/methods
//   this ticket's other tests do not happen to exercise directly (error
//   String() methods, nil-safety helpers, edge cases in the extraction
//   helpers) — keeps ./internal/runtime/... at or above Art.4's 85% core
//   floor without inflating the scenario-level test files above.

func TestErrorMethods_NonEmptyStrings(t *testing.T) {
	errs := []error{
		&DottedPathError{Path: "a.b", Reason: "bad", Suggestion: "a.c"},
		&DottedPathError{Path: "a.b", Reason: "bad"},
		&LiteralError{Raw: "x", Hint: "hint"},
		&SecretLiteralError{Field: "a.b", Reason: "reason"},
		&EditError{Path: "a.b", Reason: "reason"},
	}
	for _, err := range errs {
		if err.Error() == "" {
			t.Errorf("%T.Error() returned empty string", err)
		}
	}
}

func TestIntPtrEqual(t *testing.T) {
	a, b := 5, 5
	c := 6
	cases := []struct {
		a, b *int
		want bool
	}{
		{nil, nil, true},
		{nil, &a, false},
		{&a, nil, false},
		{&a, &b, true},
		{&a, &c, false},
	}
	for _, tc := range cases {
		if got := intPtrEqual(tc.a, tc.b); got != tc.want {
			t.Errorf("intPtrEqual(%v,%v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestLoggingEqual(t *testing.T) {
	a := loggingSection{Level: "info", Format: "json"}
	b := loggingSection{Level: "info", Format: "json"}
	if !loggingEqual(a, b) {
		t.Fatal("expected equal")
	}
	c := loggingSection{Level: "debug", Format: "json"}
	if loggingEqual(a, c) {
		t.Fatal("expected not equal (level differs)")
	}
	sz1, sz2 := 10, 10
	files1, files2 := 3, 3
	d := loggingSection{Level: "info", Format: "json", Rotation: loggingRotation{MaxSizeMB: &sz1, MaxFiles: &files1}}
	e := loggingSection{Level: "info", Format: "json", Rotation: loggingRotation{MaxSizeMB: &sz2, MaxFiles: &files2}}
	if !loggingEqual(d, e) {
		t.Fatal("expected equal rotation values via different pointers")
	}
}

func TestElevationChanged_NilSafety(t *testing.T) {
	if elevationChanged(nil, nil) {
		t.Fatal("nil,nil must report unchanged")
	}
	cfg := &Config{}
	if elevationChanged(nil, cfg) || elevationChanged(cfg, nil) {
		t.Fatal("a nil side must report unchanged, not panic or false-positive")
	}
}

func TestExtractEffectiveConfig_Nil(t *testing.T) {
	ec := extractEffectiveConfig(nil)
	if ec.Sync.Classes == nil {
		t.Fatal("expected a non-nil empty Classes map for a nil Config")
	}
}

func TestStringAt_MissingSection(t *testing.T) {
	if got := stringAt(nil, "policy", "autonomy_profile"); got != "" {
		t.Fatalf("got %q", got)
	}
	extra := map[string]interface{}{"policy": map[string]interface{}{"autonomy_profile": 5}}
	if got := stringAt(extra, "policy", "autonomy_profile"); got != "" {
		t.Fatalf("non-string value must resolve to empty, got %q", got)
	}
}

func TestStringMapAt_SkipsNonStringValues(t *testing.T) {
	extra := map[string]interface{}{"sync": map[string]interface{}{
		"memory": "local-only",
		"weird":  42,
	}}
	m := stringMapAt(extra, "sync")
	if m["memory"] != "local-only" {
		t.Fatalf("got %v", m)
	}
	if _, ok := m["weird"]; ok {
		t.Fatal("non-string value must be skipped")
	}
}

func TestDiscardEventPublisher_NeverPanics(_ *testing.T) {
	DiscardEventPublisher{}.Publish(context.Background(), "x", map[string]interface{}{"a": 1})
}

func TestStoreAuditRecorder_NilRecorderIsNoop(t *testing.T) {
	var r *StoreAuditRecorder
	if err := r.Record(context.Background(), "kind", nil); err != nil {
		t.Fatalf("nil recorder must be a no-op success, got %v", err)
	}
}

func TestNewBaselineChecker_NilEventsDefaultsToNoop(t *testing.T) {
	bc := NewBaselineChecker(nil, NewFixedClock(time.Unix(0, 0)), nil, nil)
	result := bc.Check(context.Background(), EffectiveConfig{})
	if result.Outcome != BaselineMissing {
		t.Fatalf("expected BaselineMissing with a nil store, got %v", result.Outcome)
	}
}

func TestConfigWriter_Unset_UnknownKeyErrors(t *testing.T) {
	w := &ConfigWriter{Path: t.TempDir() + "/config.toml"}
	if _, err := w.Unset("totally.unknown.key"); err == nil {
		t.Fatal("expected error for unknown key")
	}
}
