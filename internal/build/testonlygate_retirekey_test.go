package build

// Purpose: the exemption-key rules of the test-only gate. retire_ticket is a
// plan ticket id the closed manifest lists as known, a capability id with an
// expires ticket, or UNOWNED; nothing else. Each refusal below is the entry a
// weaker gate would accept, next to a control one field away that passes.

import (
	"path/filepath"
	"strings"
	"testing"
)

const (
	capabilityIDsPath      = "testdata/capability-ids.txt"
	capKeyedBaselinePath   = "testdata/testonly-capkeyed-baseline.txt"
	fixtureOpenTicket      = "P9-FIX-01"
	fixtureClosedTicket    = "P9-FIX-02"
	fixtureExpiryTicket    = "P9-FIX-03"
	fixtureCapability      = "cap:fixture-gates"
	fixtureOtherCapability = "cap:fixture-other"
)

// keyFixtureSets returns the known, capability and closed sets every
// CheckRetireKeys case below runs against.
func keyFixtureSets() (known, caps, closed map[string]bool) {
	known = map[string]bool{fixtureOpenTicket: true, fixtureClosedTicket: true, fixtureExpiryTicket: true}
	caps = map[string]bool{fixtureCapability: true}
	closed = map[string]bool{fixtureClosedTicket: true}
	return known, caps, closed
}

// capEntry is a well-formed capability-keyed entry.
func capEntry(symbol, capID, expires string) TestOnlyAllowEntry {
	e := validEntry(symbol)
	e.RetireTicket, e.Expires = capID, expires
	return e
}

// checkOne runs CheckRetireKeys over a single entry.
func checkOne(e TestOnlyAllowEntry) []string {
	known, caps, closed := keyFixtureSets()
	return CheckRetireKeys(map[string]TestOnlyAllowEntry{e.Symbol: e}, known, caps, closed)
}

// wantOneProblem fails unless problems holds exactly one line naming want.
func wantOneProblem(t *testing.T, problems []string, want string) {
	t.Helper()
	if len(problems) != 1 || !strings.Contains(problems[0], want) {
		t.Fatalf("problems = %q, want exactly one naming %q", problems, want)
	}
}

func TestRetireKey_RejectsLegacyID(t *testing.T) {
	for _, key := range []string{
		"P1-E17-W4-S36-T2", "S-01.T1", "E-08.T4", "AG/S-01.T1", "P1-E17-W4-S36-T2 (see reason)",
		" " + fixtureOpenTicket, fixtureOpenTicket + " ", "p9-fix-01", "cap:Upper", "cap:",
	} {
		t.Run(key, func(t *testing.T) {
			e := validEntry("internal/alpha.Hook")
			e.RetireTicket = key
			err := validateTicketAndCallerSite(e)
			if err == nil || !strings.Contains(err.Error(), "retire_ticket") {
				t.Fatalf("retire_ticket %q: err = %v, want a retire_ticket refusal", key, err)
			}
		})
	}
	t.Run("added_by_ticket legacy id", func(t *testing.T) {
		e := validEntry("internal/alpha.Hook")
		e.AddedByTicket = "P1-E17-W4-S36-T2"
		if err := validateTicketAndCallerSite(e); err == nil || !strings.Contains(err.Error(), "added_by_ticket") {
			t.Fatalf("err = %v, want an added_by_ticket refusal", err)
		}
	})
	t.Run("through the loader", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "allow.json")
		writeFileT(t, path, `[{"symbol":"internal/alpha.Hook","reason":"r","expected_caller":"c",`+
			`"retire_ticket":"P1-E17-W4-S36-T2","caller_site":"internal/alpha/caller.go"}]`)
		if _, err := LoadTestOnlyAllowList(path); err == nil || !strings.Contains(err.Error(), "retire_ticket") {
			t.Fatalf("the loader must refuse a legacy retire_ticket, got %v", err)
		}
	})
	t.Run("control: plan id, capability and UNOWNED pass", func(t *testing.T) {
		for _, e := range []TestOnlyAllowEntry{validEntry("internal/alpha.Hook"),
			capEntry("internal/alpha.Hook", fixtureCapability, fixtureExpiryTicket)} {
			if err := validateTicketAndCallerSite(e); err != nil {
				t.Fatalf("%q: %v", e.RetireTicket, err)
			}
		}
		e := validEntry("internal/alpha.Hook")
		e.RetireTicket = UnownedTicket
		if err := validateTicketAndCallerSite(e); err != nil {
			t.Fatalf("UNOWNED: %v", err)
		}
	})
}

func TestRetireKey_RejectsUnknownPEWTTID(t *testing.T) {
	e := validEntry("internal/alpha.Hook")
	e.RetireTicket = "P9-FIX-99"
	if err := validateTicketAndCallerSite(e); err != nil {
		t.Fatalf("the shape is valid, so the loader accepts it: %v", err)
	}
	wantOneProblem(t, checkOne(e), "P9-FIX-99")

	e = validEntry("internal/alpha.Hook")
	e.AddedByTicket = "P9-FIX-98"
	wantOneProblem(t, checkOne(e), "P9-FIX-98")

	if got := checkOne(validEntry("internal/alpha.Hook")); len(got) != 0 {
		t.Fatalf("control: a known open ticket must pass, got %q", got)
	}
	_, caps, closed := keyFixtureSets()
	e = validEntry("internal/alpha.Hook")
	if got := CheckRetireKeys(map[string]TestOnlyAllowEntry{e.Symbol: e}, nil, caps, closed); len(got) != 1 {
		t.Fatalf("an empty known set must refuse every plan id, got %q", got)
	}
}

func TestRetireKey_CapRequiresExpires(t *testing.T) {
	cases := []struct {
		name, ticket, expires, want string
	}{
		{"cap without expires", fixtureCapability, "", "expires"},
		{"cap with a legacy expires", fixtureCapability, "P1-E17-W4-S36-T2", "expires"},
		{"cap with a capability as expires", fixtureCapability, fixtureCapability, "expires"},
		{"expires on a plan-id row", fixtureOpenTicket, fixtureExpiryTicket, "expires"},
		{"expires on an UNOWNED row", UnownedTicket, fixtureExpiryTicket, "expires"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := capEntry("internal/alpha.Hook", c.ticket, c.expires)
			err := validateTicketAndCallerSite(e)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want a refusal naming %q", err, c.want)
			}
		})
	}
	if err := validateTicketAndCallerSite(capEntry("internal/alpha.Hook", fixtureCapability,
		fixtureExpiryTicket)); err != nil {
		t.Fatalf("control: a cap row with a plan-id expires must pass: %v", err)
	}
}

func TestRetireKey_CapMustBeKnownCapability(t *testing.T) {
	wantOneProblem(t, checkOne(capEntry("internal/alpha.Hook", fixtureOtherCapability, fixtureExpiryTicket)),
		fixtureOtherCapability)
	if got := checkOne(capEntry("internal/alpha.Hook", fixtureCapability, fixtureExpiryTicket)); len(got) != 0 {
		t.Fatalf("control: a listed capability must pass, got %q", got)
	}
	known, _, closed := keyFixtureSets()
	e := capEntry("internal/alpha.Hook", fixtureCapability, fixtureExpiryTicket)
	if got := CheckRetireKeys(map[string]TestOnlyAllowEntry{e.Symbol: e}, known, nil, closed); len(got) != 1 {
		t.Fatalf("an empty capability set must refuse every cap row, got %q", got)
	}
}

func TestAllowRow_ExpiredWhenExpiresTicketClosed(t *testing.T) {
	wantOneProblem(t, checkOne(capEntry("internal/alpha.Hook", fixtureCapability, fixtureClosedTicket)), "expired")
	wantOneProblem(t, checkOne(capEntry("internal/alpha.Hook", fixtureCapability, "P9-FIX-97")), "P9-FIX-97")
	if got := checkOne(capEntry("internal/alpha.Hook", fixtureCapability, fixtureExpiryTicket)); len(got) != 0 {
		t.Fatalf("control: an open expires ticket must pass, got %q", got)
	}
}

func TestCapKeyedCountNeverGrows(t *testing.T) {
	t.Run("seeded increase is refused", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "baseline.txt")
		writeFileT(t, path, "1\n")
		allow := map[string]TestOnlyAllowEntry{
			"internal/a.One": capEntry("internal/a.One", fixtureCapability, fixtureExpiryTicket),
			"internal/a.Two": capEntry("internal/a.Two", fixtureCapability, fixtureExpiryTicket),
			"internal/a.Own": validEntry("internal/a.Own"),
		}
		if got := CapKeyedCount(allow); got != 2 {
			t.Fatalf("CapKeyedCount = %d, want 2", got)
		}
		if _, err := CheckCountBaseline(path, CapKeyedCount(allow)); err == nil {
			t.Fatal("a cap-keyed count above its baseline must be refused")
		}
		if b, err := CheckCountBaseline(path, 1); err != nil || b != 1 {
			t.Fatalf("control: a count equal to the baseline passes, got %d, %v", b, err)
		}
		writeFileT(t, path, "one\n")
		if _, err := CheckCountBaseline(path, 0); err == nil {
			t.Fatal("a malformed baseline must be refused")
		}
		if _, err := CheckCountBaseline(filepath.Join(t.TempDir(), "absent.txt"), 0); err == nil {
			t.Fatal("a missing baseline must be refused")
		}
	})
	t.Run("real tree", func(t *testing.T) {
		root, allow, _ := loadRealAllowAndClosed(t)
		got := CapKeyedCount(allow)
		baseline, err := CheckCountBaseline(filepath.Join(root, "internal", "build", capKeyedBaselinePath), got)
		if err != nil {
			t.Fatalf("test-only gate: %v. A cap-keyed row is closed debt; it may be wired or deleted, never added.", err)
		}
		if got < baseline {
			t.Logf("test-only gate: cap-keyed rows are down to %d from %d. Lower %s.", got, baseline, capKeyedBaselinePath)
		}
	})
}

func TestAllowRetireKeysAreKnownTickets(t *testing.T) {
	root, allow, closed := loadRealAllowAndClosed(t)
	dir := filepath.Join(root, "internal", "build")
	known, err := LoadKnownTicketIDs(filepath.Join(dir, closedTicketManifestPath))
	if err != nil {
		t.Fatalf("loading the known ticket list: %v", err)
	}
	caps, err := LoadCapabilityIDs(filepath.Join(dir, capabilityIDsPath))
	if err != nil {
		t.Fatalf("loading capability ids: %v", err)
	}
	if problems := CheckRetireKeys(allow, known, caps, closed); len(problems) > 0 {
		t.Fatalf("test-only gate: %d allow-list key problem(s):\n  %s", len(problems), strings.Join(problems, "\n  "))
	}
}
