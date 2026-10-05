package egressproxy

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// wantInvalidNaming asserts err is KindInvalidInput and names key.
func wantInvalidNaming(t *testing.T, err error, key string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want a KindInvalidInput error naming %q, got nil", key)
	}
	if k, ok := cascade.KindOf(err); !ok || k != cascade.KindInvalidInput {
		t.Fatalf("kind = %v (ok %v), want KindInvalidInput: %v", k, ok, err)
	}
	if !strings.Contains(err.Error(), key) {
		t.Fatalf("error %q does not name the key %q", err.Error(), key)
	}
}

// egressDoc builds an Extra map holding agents.egress = tables.
func egressDoc(tables map[string]any) map[string]any {
	return map[string]any{"agents": map[string]any{"egress": tables}}
}

func TestParseEgressAllowlistsAbsentTablesYieldEmptyLists(t *testing.T) {
	docs := []map[string]any{
		nil,
		{},
		{"agents": map[string]any{}},
		{"agents": map[string]any{"other": "kept for another owner"}},
		egressDoc(map[string]any{}),
		egressDoc(map[string]any{"codex": map[string]any{}}),
		egressDoc(map[string]any{"codex": map[string]any{"allow": []any{}}}),
	}
	for i, doc := range docs {
		got, err := ParseEgressAllowlists(doc)
		if err != nil {
			t.Fatalf("doc %d: unexpected error %v", i, err)
		}
		if len(got) != len(knownDrivers) {
			t.Fatalf("doc %d: %d drivers, want every one of %d", i, len(got), len(knownDrivers))
		}
		for _, id := range knownDrivers {
			list, ok := got[id]
			if !ok || len(list) != 0 {
				t.Fatalf("doc %d: driver %s = %v (present %v), want an empty list", i, id, list, ok)
			}
		}
	}
}

func TestParseEgressAllowlistsParsesEachDriver(t *testing.T) {
	doc := egressDoc(map[string]any{
		"claude":      map[string]any{"allow": []any{"api.example.test:443", "[2001:db8::1]:8443"}},
		"codex":       map[string]any{"allow": []any{"93.184.216.34:443"}},
		"opencode":    map[string]any{"allow": []string{"models.example.test:443"}},
		"antigravity": map[string]any{"allow": []any{}},
	})
	got, err := ParseEgressAllowlists(doc)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	want := map[DriverID][]Destination{
		DriverClaude:      {"api.example.test:443", "[2001:db8::1]:8443"},
		DriverCodex:       {"93.184.216.34:443"},
		DriverOpenCode:    {"models.example.test:443"},
		DriverAntigravity: {},
	}
	for id, w := range want {
		g := got[id]
		if len(g) != len(w) {
			t.Fatalf("%s: got %v, want %v", id, g, w)
		}
		for i := range w {
			if g[i] != w[i] {
				t.Fatalf("%s[%d] = %q, want %q", id, i, g[i], w[i])
			}
		}
	}
}

func TestParseEgressAllowlistsRefusesUnknownDriver(t *testing.T) {
	for _, id := range []string{"gemini", "Claude", "claude ", ""} {
		_, err := ParseEgressAllowlists(egressDoc(map[string]any{id: map[string]any{"allow": []any{}}}))
		wantInvalidNaming(t, err, "agents.egress."+id)
	}
}

func TestParseEgressAllowlistsRefusesUnknownKey(t *testing.T) {
	for _, key := range []string{"deny", "Allow", "allow_all", "wildcard"} {
		doc := egressDoc(map[string]any{"claude": map[string]any{"allow": []any{}, key: true}})
		_, err := ParseEgressAllowlists(doc)
		wantInvalidNaming(t, err, "agents.egress.claude."+key)
	}
}

func TestParseEgressAllowlistsRefusesMalformedEntries(t *testing.T) {
	bad := []string{
		"*.example.test:443", "*:443", "http://api.example.test:443", "api.example.test:443/v1",
		"u@api.example.test:443", "api.example.test.:443", "api.example.test", "api.example.test:",
		"api.example.test:0443", "api.example.test:0", "api.example.test:65536", "API.example.test:443",
		"[fe80::1%en0]:443", "fe80::1:443", "[::ffff:127.0.0.1]:443", "[2001:DB8::1]:443",
		".example.test:443", "example..test:443", "-api.example.test:443", "127.1:443",
		"127.000.0.1:443", "api_x.example.test:443", "api.example.test:+443", "api.example.test:44 3",
		"10.0.0.0/8:443", "", "[2001:db8::1]", "[]:443", "[1.2.3.4]:443",
	}
	for _, entry := range bad {
		doc := egressDoc(map[string]any{"codex": map[string]any{"allow": []any{"ok.example.test:443", entry}}})
		_, err := ParseEgressAllowlists(doc)
		wantInvalidNaming(t, err, "agents.egress.codex.allow[1]")
	}
}

func TestParseEgressAllowlistsRefusesWrongShapes(t *testing.T) {
	cases := []struct {
		doc map[string]any
		key string
	}{
		{map[string]any{"agents": "x"}, "agents"},
		{map[string]any{"agents": map[string]any{"egress": []any{}}}, "agents.egress"},
		{egressDoc(map[string]any{"claude": []any{"api.example.test:443"}}), "agents.egress.claude"},
		{egressDoc(map[string]any{"claude": map[string]any{"allow": "api.example.test:443"}}), "agents.egress.claude.allow"},
	}
	for _, c := range cases {
		_, err := ParseEgressAllowlists(c.doc)
		wantInvalidNaming(t, err, c.key)
	}
}

func TestParseEgressAllowlistsRefusesNonStringEntry(t *testing.T) {
	for _, entry := range []any{443, nil, map[string]any{}, []any{"a:1"}} {
		doc := egressDoc(map[string]any{"opencode": map[string]any{"allow": []any{"api.example.test:443", entry}}})
		_, err := ParseEgressAllowlists(doc)
		wantInvalidNaming(t, err, "agents.egress.opencode.allow")
	}
}

func TestParseEgressAllowlistsNamesFirstBadKeyDeterministically(t *testing.T) {
	doc := egressDoc(map[string]any{
		"codex":  map[string]any{"bogus": 1},
		"claude": map[string]any{"allow": []any{"*:443"}},
		"zeta":   map[string]any{},
	})
	for i := 0; i < 20; i++ {
		_, err := ParseEgressAllowlists(doc)
		wantInvalidNaming(t, err, "agents.egress.claude.allow[0]")
	}
}

func TestParseDestinationAcceptsCanonicalForms(t *testing.T) {
	for _, s := range []string{
		"api.example.test:443", "localhost:1", "a:65535", "93.184.216.34:443",
		"[2001:db8::1]:443", "x-1.example.test:8443", "api.example.test:9",
	} {
		d, err := ParseDestination(s)
		if err != nil || string(d) != s {
			t.Fatalf("ParseDestination(%q) = %q, %v; want it unchanged", s, d, err)
		}
	}
	if _, err := ParseDestination(strings.Repeat("a", 254) + ":443"); err == nil {
		t.Fatal("a 254-byte name was accepted")
	}
	if _, err := ParseDestination(strings.Repeat("a", 64) + ".test:443"); err == nil {
		t.Fatal("a 64-byte label was accepted")
	}
}
