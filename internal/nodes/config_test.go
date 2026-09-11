package nodes

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestParseSectionAbsent(t *testing.T) {
	sec, err := parseSection(nil)
	if err != nil {
		t.Fatalf("unexpected error for absent section: %v", err)
	}
	if sec != (Section{}) {
		t.Fatalf("expected zero Section, got %+v", sec)
	}
}

func TestParseSectionValid(t *testing.T) {
	raw := map[string]interface{}{
		"default_trust_tier": "worker-trusted",
		"known_hosts_path":   "/custom/path.json",
	}
	sec, err := parseSection(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sec.DefaultTrustTier != "worker-trusted" {
		t.Fatalf("DefaultTrustTier = %q", sec.DefaultTrustTier)
	}
	if sec.KnownHostsPath != "/custom/path.json" {
		t.Fatalf("KnownHostsPath = %q", sec.KnownHostsPath)
	}
}

func TestParseSectionInvalidTierRefused(t *testing.T) {
	raw := map[string]interface{}{"default_trust_tier": "super-admin"}
	_, err := parseSection(raw)
	if err == nil {
		t.Fatal("expected refusal for invalid default_trust_tier")
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindInvalidInput {
		t.Fatalf("expected KindInvalidInput, got %v (ok=%v)", k, ok)
	}
}

func TestParseSectionWrongType(t *testing.T) {
	raw := map[string]interface{}{"default_trust_tier": 42}
	if _, err := parseSection(raw); err == nil {
		t.Fatal("expected refusal for non-string default_trust_tier")
	}

	raw2 := map[string]interface{}{"known_hosts_path": 42}
	if _, err := parseSection(raw2); err == nil {
		t.Fatal("expected refusal for non-string known_hosts_path")
	}
}
