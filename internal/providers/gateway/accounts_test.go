package gateway

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func accountTable() map[string]any {
	return map[string]any{"base_url": "https://gateway.invalid", "key_env": "TEST_KEY"}
}

func parseTable(t *testing.T, m map[string]any, badKey string) []Account {
	t.Helper()
	a, err := ParseAccounts(map[string]any{"alpha": m})
	if badKey == "" {
		if err != nil {
			t.Fatal(err)
		}
	} else if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), badKey) {
		t.Fatalf("expected invalid key %s, got %v", badKey, err)
	}
	return a
}

func TestParseAccountsRefusesUnknownKey(t *testing.T) {
	m := accountTable()
	m["unknown"] = true
	parseTable(t, m, "unknown")
}

func TestParseAccountsExactlyOneCredential(t *testing.T) {
	m := accountTable()
	m["credential_helper"] = filepath.Join(t.TempDir(), "program") // absolute on every platform
	parseTable(t, m, "exactly one")
	delete(m, "key_env")
	delete(m, "credential_helper")
	parseTable(t, m, "exactly one")
}

func TestParseAccountsHTTPSOnly(t *testing.T) {
	for _, base := range []string{"http://gateway.invalid", "ftp://gateway.invalid", "https:///path", "https://user@gateway.invalid", "https://gateway.invalid?q=x", "https://gateway.invalid/#fragment", "https://%"} {
		m := accountTable()
		m["base_url"] = base
		parseTable(t, m, "base_url")
	}
	for _, base := range []string{"http://127.0.0.1", "http://[::1]", "http://localhost"} {
		m := accountTable()
		m["base_url"] = base
		parseTable(t, m, "")
	}
}

func TestParseAccountsHelperNeedsDigest(t *testing.T) {
	m := accountTable()
	delete(m, "key_env")
	m["credential_helper"] = "/owner/program"
	parseTable(t, m, "helper_sha256")
	for _, digest := range []string{"bad", strings.Repeat("A", 64)} {
		m["helper_sha256"] = digest
		parseTable(t, m, "helper_sha256")
	}
	m["helper_sha256"] = strings.Repeat("a", 64)
	parseTable(t, m, "")
	m["credential_helper"] = "relative"
	parseTable(t, m, "credential_helper")
}

func TestAccountIDNormalized(t *testing.T) {
	for _, c := range []struct{ base, id string }{
		{"HTTPS://Gateway.INVALID/", "https://gateway.invalid"},
		{"https://gateway.invalid:443/v1/", "https://gateway.invalid/v1"},
		{"http://LOCALHOST:80/path", "http://localhost/path"},
		{"http://127.0.0.1:8080/", "http://127.0.0.1:8080"},
		{"https://[::1]:443/", "https://[::1]"},
		{"https://[::1]:8080/", "https://[::1]:8080"},
	} {
		m := accountTable()
		m["base_url"] = c.base
		a := parseTable(t, m, "")
		if a[0].ID != c.id {
			t.Fatalf("wrong ID: %s", a[0].ID)
		}
	}
}

func TestParseAccountsUsageSource(t *testing.T) {
	for _, c := range []struct {
		key   string
		value any
		bad   string
	}{
		{"source", "magic", "source"}, {"window", "hour", "window"}, {"unknown", true, "unknown"},
		{"path", "https://gateway.invalid", "path"}, {"path", "//gateway.invalid/usage", "path"},
		{"used_pointer", "bad", "used_pointer"}, {"limit_pointer", "/~2", "limit_pointer"},
		{"resets_at_pointer", 42, "resets_at_pointer"}, {"utilization_header", "x-used", "utilization_header"},
	} {
		m := accountTable()
		u := map[string]any{"source": "endpoint", "window": "day", "path": "/usage"}
		u[c.key] = c.value
		m["usage"] = u
		parseTable(t, m, c.bad)
	}
	m := accountTable()
	m["usage"] = map[string]any{"source": "endpoint", "window": "5h", "path": "/usage", "used_pointer": "/~0/~1", "limit_pointer": ""}
	parseTable(t, m, "")
	m["usage"] = map[string]any{"source": "headers", "window": "month", "utilization_header": "x-used", "resets_header": "x-reset"}
	parseTable(t, m, "")
	m["usage"] = map[string]any{"source": "headers", "window": "week", "resets_header": "bad\r\n"}
	parseTable(t, m, "resets_header")
	m["usage"] = 42
	parseTable(t, m, "usage")
}

func TestParseAccountsValidation(t *testing.T) {
	for _, c := range []struct {
		key   string
		value any
	}{
		{"base_url", 42}, {"base_url", ""}, {"key_env", ""}, {"key_env", 42}, {"key_env", "BAD-NAME"},
		{"header", "unknown"}, {"header", 42}, {"models", 42}, {"models", []any{42}}, {"models", []any{""}},
		{"helper_sha256", "unused"},
	} {
		m := accountTable()
		m[c.key] = c.value
		parseTable(t, m, c.key)
	}
	for _, name := range []string{"Bad", "-bad", strings.Repeat("a", 33)} {
		if _, err := ParseAccounts(map[string]any{name: accountTable()}); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatal("bad name accepted")
		}
	}
	if _, err := ParseAccounts(map[string]any{"alpha": 42}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatal("non-table accepted")
	}
	m := accountTable()
	m["header"] = "x-api-key"
	m["models"] = []string{"model-a"}
	if a := parseTable(t, m, ""); a[0].Catalog() != CatalogDeclared {
		t.Fatal("wrong catalog")
	}
	m["models"] = []any{}
	if a := parseTable(t, m, ""); a[0].Catalog() != CatalogDeclared {
		t.Fatal("empty declared catalog")
	}
	delete(m, "models")
	if a := parseTable(t, m, ""); a[0].Catalog() != CatalogDiscovered {
		t.Fatal("wrong discovery")
	}
}

func TestAdmittedAccountsAndLookup(t *testing.T) {
	a, err := ParseAccounts(map[string]any{"beta": accountTable(), "alpha": accountTable()})
	if err != nil || a[0].Name != "alpha" || !a[0].TermsSensitive {
		t.Fatal("sort or posture")
	}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	got, err := AccountsFrom(runtime.EffectiveConfig{Families: map[string]json.RawMessage{"gateways": raw}})
	if err != nil || len(got) != 2 {
		t.Fatalf("admitted accounts: %v", err)
	}
	if found, err := Lookup(got, "alpha"); err != nil || found.Name != "alpha" {
		t.Fatal("lookup failed")
	}
	if _, err := Lookup(got, "absent"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatal("missing lookup")
	}
	if _, err := AccountsFrom(runtime.EffectiveConfig{Families: map[string]json.RawMessage{"gateways": json.RawMessage("{}")}}); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatal("bad payload")
	}
	if got, err := AccountsFrom(runtime.EffectiveConfig{}); err != nil || len(got) != 0 {
		t.Fatal("absent family")
	}
}
