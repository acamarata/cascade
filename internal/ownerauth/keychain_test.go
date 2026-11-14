package ownerauth

import (
	"reflect"
	"testing"
)

// TestOwnerCredentialFieldsExist verifies that OwnerCredential has the required fields.
func TestOwnerCredentialFieldsExist(t *testing.T) {
	c := OwnerCredential{}
	v := reflect.ValueOf(c)
	typ := v.Type()

	expectedFields := map[string]string{
		"Service": "string",
		"Account": "string",
		"Token":   "string",
	}

	for fieldName, expectedType := range expectedFields {
		field, found := typ.FieldByName(fieldName)
		if !found {
			t.Errorf("OwnerCredential missing field %s", fieldName)
			continue
		}
		if field.Type.String() != expectedType {
			t.Errorf("OwnerCredential.%s has type %s, want %s", fieldName, field.Type.String(), expectedType)
		}
	}
}

// TestApprovalRequestFieldsExist verifies that ApprovalRequest has the required fields.
func TestApprovalRequestFieldsExist(t *testing.T) {
	a := ApprovalRequest{}
	v := reflect.ValueOf(a)
	typ := v.Type()

	expectedFields := map[string]string{
		"SessionID": "string",
		"Action":    "string",
	}

	for fieldName, expectedType := range expectedFields {
		field, found := typ.FieldByName(fieldName)
		if !found {
			t.Errorf("ApprovalRequest missing field %s", fieldName)
			continue
		}
		if field.Type.String() != expectedType {
			t.Errorf("ApprovalRequest.%s has type %s, want %s", fieldName, field.Type.String(), expectedType)
		}
	}
}

// TestApprovalResultFieldsExist verifies that ApprovalResult has the required fields.
func TestApprovalResultFieldsExist(t *testing.T) {
	r := ApprovalResult{}
	v := reflect.ValueOf(r)
	typ := v.Type()

	expectedFields := map[string]string{
		"Granted": "bool",
		"Reason":  "string",
	}

	for fieldName, expectedType := range expectedFields {
		field, found := typ.FieldByName(fieldName)
		if !found {
			t.Errorf("ApprovalResult missing field %s", fieldName)
			continue
		}
		if field.Type.String() != expectedType {
			t.Errorf("ApprovalResult.%s has type %s, want %s", fieldName, field.Type.String(), expectedType)
		}
	}
}
