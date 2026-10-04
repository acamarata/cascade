// Purpose: allowlist_test.go proves TelemetryOutcome's actual field set
//
//	and allowlist.go's allowedOutcomeFields never drift apart -- a field
//	absent from the allowlist is THIS test failing, not a silent write.
//
// SPORT: learn/allowlist/ADD (P1-E31-W6-S64-T1).
package learn

import (
	"reflect"
	"sort"
	"testing"
)

// TestAllowlistMatchesStruct: every exported TelemetryOutcome field name
// is in sortedOutcomeFields(), and vice versa -- exact set equality.
func TestAllowlistMatchesStruct(t *testing.T) {
	typ := reflect.TypeOf(TelemetryOutcome{})
	var structFields []string
	for i := 0; i < typ.NumField(); i++ {
		structFields = append(structFields, typ.Field(i).Name)
	}
	sort.Strings(structFields)
	allowed := sortedOutcomeFields()
	if len(structFields) != len(allowed) {
		t.Fatalf("TelemetryOutcome has %d fields, allowlist has %d: struct=%v allowlist=%v",
			len(structFields), len(allowed), structFields, allowed)
	}
	for i := range structFields {
		if structFields[i] != allowed[i] {
			t.Errorf("field set mismatch at index %d: struct=%q allowlist=%q", i, structFields[i], allowed[i])
		}
	}
}

// TestValidateOutcomeAllowlistAcceptsRealStruct: a normally-constructed
// TelemetryOutcome always validates clean.
func TestValidateOutcomeAllowlistAcceptsRealStruct(t *testing.T) {
	if err := validateOutcomeAllowlist(baseOutcome("job-allow-1")); err != nil {
		t.Errorf("validateOutcomeAllowlist rejected a well-formed TelemetryOutcome: %v", err)
	}
}
