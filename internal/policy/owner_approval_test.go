package policy

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestOwnerApprovalValidate(t *testing.T) {
	tests := []struct {
		name     string
		approval OwnerApproval
		wantErr  bool
	}{
		{
			name: "complete record passes",
			approval: OwnerApproval{
				GrantID:     cascade.ID("grant-123"),
				IssuedAt:    time.Now(),
				Attestation: []byte("sig"),
			},
			wantErr: false,
		},
		{
			name: "invalid GrantID",
			approval: OwnerApproval{
				GrantID:     "",
				IssuedAt:    time.Now(),
				Attestation: []byte("sig"),
			},
			wantErr: true,
		},
		{
			name: "zero IssuedAt",
			approval: OwnerApproval{
				GrantID:     cascade.ID("grant-123"),
				IssuedAt:    time.Time{},
				Attestation: []byte("sig"),
			},
			wantErr: true,
		},
		{
			name: "empty Attestation",
			approval: OwnerApproval{
				GrantID:     cascade.ID("grant-123"),
				IssuedAt:    time.Now(),
				Attestation: []byte{},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.approval.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !errors.Is(err, ErrOwnerApprovalInvalid) {
					t.Fatalf("expected ErrOwnerApprovalInvalid, got %v", err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestOwnerApprovalHasNoExpiryAndNoSwitch(t *testing.T) {
	typ := reflect.TypeOf(OwnerApproval{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.ToLower(field.Name)
		if name == "exp" || name == "expiresat" || name == "bypass" {
			t.Errorf("OwnerApproval must not have field %q", field.Name)
		}
		if field.Type.Kind() == reflect.Bool {
			t.Errorf("OwnerApproval must not have bool field, found %q", field.Name)
		}
	}
}
