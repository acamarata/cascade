package policy

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseEffectClassClosed(t *testing.T) {
	tests := []struct {
		in   string
		want EffectClass
	}{
		{"credential", EffectCredential},
		{"spend", EffectSpend},
		{"legal_terms", EffectLegalTerms},
		{"irreversible_destructive", EffectIrreversibleDestructive},
		{"external_outbound", EffectExternalOutbound},
		{"public_identity", EffectPublicIdentity},
	}
	for _, tt := range tests {
		ec, err := ParseEffectClass(tt.in)
		if err != nil {
			t.Errorf("ParseEffectClass(%q) returned error: %v", tt.in, err)
			continue
		}
		if ec != tt.want {
			t.Errorf("ParseEffectClass(%q) = %v, want %v", tt.in, ec, tt.want)
		}
	}

	_, err := ParseEffectClass("unknown_class")
	if err == nil {
		t.Fatal("expected error for unknown effect class, got nil")
	}
	if !errors.Is(err, ErrUnknownEffectClass) && !strings.Contains(err.Error(), "unknown_effect_class") {
		t.Fatalf("expected unknown_effect_class error, got %v", err)
	}
}

func TestAuthorityRequestHasNoBypassField(t *testing.T) {
	checkNoBypass := func(v interface{}, name string) {
		typ := reflect.TypeOf(v)
		if typ.Kind() == reflect.Ptr {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct {
			t.Fatalf("%s is not a struct", name)
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Type.Kind() == reflect.Bool {
				t.Fatalf("%s has bool field: %s", name, field.Name)
			}
			if strings.EqualFold(field.Name, "bypass") {
				t.Fatalf("%s has bypass field: %s", name, field.Name)
			}
		}
	}

	checkNoBypass(AuthorityRequest{}, "AuthorityRequest")
	checkNoBypass(AuthorityGrant{}, "AuthorityGrant")
}
