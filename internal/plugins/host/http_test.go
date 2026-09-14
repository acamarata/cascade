package host

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestCheckHTTP(t *testing.T) {
	tests := []struct {
		name    string
		scopes  []string
		url     string
		wantErr bool
	}{
		{"exact host allowed", []string{"api.example.com"}, "https://api.example.com/v1", false},
		{"host outside scope denied", []string{"api.example.com"}, "https://evil.example.net/v1", true},
		{"wildcard subdomain allowed", []string{"*.example.com"}, "https://api.example.com/v1", false},
		{"wildcard does not cover bare domain", []string{"*.example.com"}, "https://example.com/v1", true},
		{"empty scopes deny everything", nil, "https://api.example.com/v1", true},
		{"malformed URL denies", []string{"api.example.com"}, "://not a url", true},
		{"empty URL denies", []string{"api.example.com"}, "", true},
		{"scheme-only URL with no host denies", []string{"api.example.com"}, "file:///etc/passwd", true},
		{"case-insensitive host match", []string{"API.EXAMPLE.COM"}, "https://api.example.com/v1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &fakeAuditSink{}
			e, err := NewHostBoundaryEnforcer("plugin-a", Grants{NetScopes: tt.scopes}, nil, nil, sink)
			if err != nil {
				t.Fatalf("build enforcer: %v", err)
			}
			gotErr := e.CheckHTTP(context.Background(), tt.url)
			if (gotErr != nil) != tt.wantErr {
				t.Fatalf("CheckHTTP(%q) error = %v, wantErr %v", tt.url, gotErr, tt.wantErr)
			}
			if tt.wantErr {
				if !errors.Is(gotErr, cascade.ErrCapabilityDenied) {
					t.Fatalf("denied error kind = %v, want KindCapabilityDenied", gotErr)
				}
				entry, ok := sink.last()
				if !ok || entry.Allowed || entry.CallType != httpCallType || entry.PluginID != "plugin-a" {
					t.Fatalf("audit entry = %+v (ok=%v), want a denied http entry for plugin-a", entry, ok)
				}
			} else if len(sink.events) != 0 {
				t.Fatalf("an allowed CheckHTTP call must not audit, got %+v", sink.events)
			}
		})
	}
}

func TestScopeMatchesExactAndWildcard(t *testing.T) {
	if !scopeMatches("api.example.com", "api.example.com") {
		t.Fatal("exact match must pass")
	}
	if scopeMatches("api.example.com", "other.example.com") {
		t.Fatal("distinct hosts must not match")
	}
	if !scopeMatches("*.example.com", "a.example.com") {
		t.Fatal("wildcard must cover a direct subdomain")
	}
	if !scopeMatches("*.example.com", "a.b.example.com") {
		t.Fatal("wildcard must cover a nested subdomain")
	}
	if scopeMatches("*.example.com", "example.com") {
		t.Fatal("wildcard must not cover the bare apex domain")
	}
	if scopeMatches("", "example.com") {
		t.Fatal("an empty scope must never match")
	}
}
