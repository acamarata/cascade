package host

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestCheckStorage(t *testing.T) {
	tests := []struct {
		name        string
		domains     map[string]bool
		crossDomain bool
		domain      string
		wantErr     bool
	}{
		{"declared same-domain allowed", map[string]bool{"cache": true}, false, "cache", false},
		{"undeclared domain without cross-domain denied", map[string]bool{"cache": true}, false, "other-plugin-domain", true},
		{"undeclared domain with cross-domain allowed", map[string]bool{"cache": true}, true, "other-plugin-domain", false},
		{"empty domain always denied even with cross-domain", map[string]bool{"cache": true}, true, "", true},
		{"nil domain set denies everything without cross-domain", nil, false, "cache", true},
		{"nil domain set with cross-domain allows", nil, true, "cache", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &fakeAuditSink{}
			grants := Grants{StorageDomains: tt.domains, CrossDomain: tt.crossDomain}
			e, err := NewHostBoundaryEnforcer("plugin-a", grants, nil, nil, sink)
			if err != nil {
				t.Fatalf("build enforcer: %v", err)
			}
			gotErr := e.CheckStorage(context.Background(), tt.domain)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("CheckStorage(%q) error = %v, wantErr %v", tt.domain, gotErr, tt.wantErr)
			}
			if tt.wantErr {
				if !errors.Is(gotErr, cascade.ErrCapabilityDenied) {
					t.Fatalf("denied error kind = %v, want KindCapabilityDenied", gotErr)
				}
				entry, ok := sink.last()
				if !ok || entry.Allowed || entry.CallType != storageCallType {
					t.Fatalf("audit entry = %+v (ok=%v), want a denied storage entry", entry, ok)
				}
			} else if len(sink.events) != 0 {
				t.Fatalf("an allowed CheckStorage call must not audit, got %+v", sink.events)
			}
		})
	}
}
