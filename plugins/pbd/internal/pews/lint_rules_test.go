// Package pews (lint_rules_test.go): one test per lint rule proving it
// FIRES on a violating contract, not merely that a clean one passes.
package pews

import (
	"strings"
	"testing"
)

// mustRecord decodes yaml and wraps it as a TicketRecord with CanonicalID
// == ID, so identity-mismatch never confounds a rule-level test.
func mustRecord(t *testing.T, id, yaml string) TicketRecord {
	t.Helper()
	tk, err := DecodeTicket([]byte(yaml))
	if err != nil {
		t.Fatalf("DecodeTicket: %v", err)
	}
	return TicketRecord{ID: id, CanonicalID: id, Ticket: tk, RelPath: id + ".yaml"}
}

func TestRuleFieldConstraints(t *testing.T) {
	id := "P1-E14-W3-S28-T1"
	t.Run("clean passes", func(t *testing.T) {
		rec := mustRecord(t, id, cleanTicketYAML(id))
		if v := ruleFieldConstraints(rec); len(v) != 0 {
			t.Errorf("issues = %+v, want none", v)
		}
	})
	t.Run("empty title fires", func(t *testing.T) {
		yaml := strings.Replace(cleanTicketYAML(id), "title: Do the thing\n", "title: \"\"\n", 1)
		rec := mustRecord(t, id, yaml)
		if v := ruleFieldConstraints(rec); !hasKind(v, LintKindFieldRequired) {
			t.Errorf("issues = %+v, want LintKindFieldRequired", v)
		}
	})
	t.Run("title over 60 chars fires", func(t *testing.T) {
		long := strings.Repeat("x", 61)
		yaml := strings.Replace(cleanTicketYAML(id), "title: Do the thing\n", "title: "+long+"\n", 1)
		rec := mustRecord(t, id, yaml)
		if v := ruleFieldConstraints(rec); !hasKind(v, LintKindFieldTooLong) {
			t.Errorf("issues = %+v, want LintKindFieldTooLong", v)
		}
	})
	t.Run("malformed branch fires", func(t *testing.T) {
		yaml := strings.Replace(cleanTicketYAML(id), "branch: "+id+"-Do-The-Thing\n", "branch: not-a-branch\n", 1)
		rec := mustRecord(t, id, yaml)
		if v := ruleFieldConstraints(rec); !hasKind(v, LintKindFieldRequired) {
			t.Errorf("issues = %+v, want LintKindFieldRequired for a malformed branch", v)
		}
	})
}

func TestRuleCardinality(t *testing.T) {
	id := "P1-E14-W3-S28-T1"
	t.Run("clean passes", func(t *testing.T) {
		rec := mustRecord(t, id, cleanTicketYAML(id))
		if v := ruleCardinality(rec); len(v) != 0 {
			t.Errorf("issues = %+v, want none", v)
		}
	})
	t.Run("fewer than 3 tasks fires", func(t *testing.T) {
		yaml := strings.Replace(cleanTicketYAML(id), "tasks:\n  - step one\n  - step two\n  - step three\n", "tasks:\n  - step one\n", 1)
		rec := mustRecord(t, id, yaml)
		if v := ruleCardinality(rec); !hasKind(v, LintKindCardinality) {
			t.Errorf("issues = %+v, want LintKindCardinality", v)
		}
	})
	t.Run("empty checks fires", func(t *testing.T) {
		yaml := strings.Replace(cleanTicketYAML(id), "checks:\n  - go test ./plugins/pbd/...\n", "checks: []\n", 1)
		rec := mustRecord(t, id, yaml)
		if v := ruleCardinality(rec); !hasKind(v, LintKindCardinality) {
			t.Errorf("issues = %+v, want LintKindCardinality", v)
		}
	})
	t.Run("blank task entry fires", func(t *testing.T) {
		yaml := strings.Replace(cleanTicketYAML(id), "  - step one\n", "  - \"   \"\n", 1)
		rec := mustRecord(t, id, yaml)
		if v := ruleCardinality(rec); !hasKind(v, LintKindBlankEntry) {
			t.Errorf("issues = %+v, want LintKindBlankEntry", v)
		}
	})
}

func TestRuleDependencyForm(t *testing.T) {
	id := "P1-E14-W3-S28-T1"
	t.Run("clean (no deps) passes", func(t *testing.T) {
		rec := mustRecord(t, id, cleanTicketYAML(id))
		if v := ruleDependencyForm(rec); len(v) != 0 {
			t.Errorf("issues = %+v, want none", v)
		}
	})
	t.Run("literal id passes", func(t *testing.T) {
		yaml := strings.Replace(cleanTicketYAML(id), "depends_on: []\n", "depends_on:\n  - P1-E14-W3-S28-T2\n", 1)
		rec := mustRecord(t, id, yaml)
		if v := ruleDependencyForm(rec); len(v) != 0 {
			t.Errorf("issues = %+v, want none for a literal id", v)
		}
	})
	t.Run("shorthand form fires", func(t *testing.T) {
		yaml := strings.Replace(cleanTicketYAML(id), "depends_on: []\n", "depends_on:\n  - S-28.T2\n", 1)
		rec := mustRecord(t, id, yaml)
		if v := ruleDependencyForm(rec); !hasKind(v, LintKindDependencyForm) {
			t.Errorf("issues = %+v, want LintKindDependencyForm", v)
		}
	})
}

func TestRuleCRWeight(t *testing.T) {
	id := "P1-E14-W3-S28-T1"
	t.Run("clean (S/CR-A+CR-B) passes", func(t *testing.T) {
		rec := mustRecord(t, id, cleanTicketYAML(id))
		if v := ruleCRWeight(rec); len(v) != 0 {
			t.Errorf("issues = %+v, want none", v)
		}
	})
	t.Run("weight M below the CR-B floor fires", func(t *testing.T) {
		yaml := cleanTicketYAML(id)
		yaml = strings.Replace(yaml, "weight: S\n", "weight: M\n", 1)
		yaml = strings.Replace(yaml, "cr_level: CR-A+CR-B\n", "cr_level: CR-A\n", 1)
		rec := mustRecord(t, id, yaml)
		if v := ruleCRWeight(rec); !hasKind(v, LintKindCRWeightMismatch) {
			t.Errorf("issues = %+v, want LintKindCRWeightMismatch", v)
		}
	})
}

func TestRuleAcceptanceDoD(t *testing.T) {
	id := "P1-E14-W3-S28-T1"
	t.Run("clean passes", func(t *testing.T) {
		rec := mustRecord(t, id, cleanTicketYAML(id))
		if v := ruleAcceptanceDoD(rec); len(v) != 0 {
			t.Errorf("issues = %+v, want none", v)
		}
	})
	t.Run("missing DoD phrase fires", func(t *testing.T) {
		yaml := strings.Replace(cleanTicketYAML(id),
			`  - "checks green in CI; error-path tests present; -race clean; no Article-1 stubs; docs_updates landed; CR by a different agent; journal written"`+"\n",
			"  - \"looks fine to me\"\n", 1)
		rec := mustRecord(t, id, yaml)
		if v := ruleAcceptanceDoD(rec); !hasKind(v, LintKindMissingDoD) {
			t.Errorf("issues = %+v, want LintKindMissingDoD", v)
		}
	})
}

func TestRuleFilesScope(t *testing.T) {
	id := "P1-E14-W3-S28-T1"
	t.Run("clean passes", func(t *testing.T) {
		rec := mustRecord(t, id, cleanTicketYAML(id))
		if v := ruleFilesScope(rec); len(v) != 0 {
			t.Errorf("issues = %+v, want none", v)
		}
	})
	t.Run("empty files_scope fires", func(t *testing.T) {
		yaml := strings.Replace(cleanTicketYAML(id), "files_scope:\n  add:\n    - plugins/pbd/x.go\n  change: []\n  delete: []\n",
			"files_scope:\n  add: []\n  change: []\n  delete: []\n", 1)
		rec := mustRecord(t, id, yaml)
		if v := ruleFilesScope(rec); !hasKind(v, LintKindFilesScopeEmpty) {
			t.Errorf("issues = %+v, want LintKindFilesScopeEmpty", v)
		}
	})
}

func TestRuleJournals(t *testing.T) {
	id := "P1-E14-W3-S28-T1"
	t.Run("clean (true) passes", func(t *testing.T) {
		rec := mustRecord(t, id, cleanTicketYAML(id))
		if v := ruleJournals(rec); len(v) != 0 {
			t.Errorf("issues = %+v, want none", v)
		}
	})
	t.Run("omitted fires", func(t *testing.T) {
		yaml := strings.Replace(cleanTicketYAML(id), "journals: true\n", "", 1)
		rec := mustRecord(t, id, yaml)
		if v := ruleJournals(rec); !hasKind(v, LintKindJournalsMissing) {
			t.Errorf("issues = %+v, want LintKindJournalsMissing", v)
		}
	})
	t.Run("false fires", func(t *testing.T) {
		yaml := strings.Replace(cleanTicketYAML(id), "journals: true\n", "journals: false\n", 1)
		rec := mustRecord(t, id, yaml)
		if v := ruleJournals(rec); !hasKind(v, LintKindJournalsMissing) {
			t.Errorf("issues = %+v, want LintKindJournalsMissing", v)
		}
	})
}

func TestRuleGateOnly(t *testing.T) {
	member := canonicalIDLoc{"J", 4, 21, 3}.id() // 06 §3's gate_only closed set
	nonMember := "P1-E14-W3-S28-T1"
	gateOnly := idSet(gateOnlyLocs)

	t.Run("member without the flag fires missing", func(t *testing.T) {
		rec := mustRecord(t, member, cleanTicketYAML(member))
		if v := ruleGateOnly(rec, gateOnly); !hasKind(v, LintKindGateOnlyMissing) {
			t.Errorf("issues = %+v, want LintKindGateOnlyMissing", v)
		}
	})
	t.Run("member with the flag passes", func(t *testing.T) {
		yaml := strings.Replace(cleanTicketYAML(member), "journals: true\n", "journals: true\ngate_only: true\n", 1)
		rec := mustRecord(t, member, yaml)
		if v := ruleGateOnly(rec, gateOnly); len(v) != 0 {
			t.Errorf("issues = %+v, want none", v)
		}
	})
	t.Run("non-member with the flag fires unauthorized", func(t *testing.T) {
		yaml := strings.Replace(cleanTicketYAML(nonMember), "journals: true\n", "journals: true\ngate_only: true\n", 1)
		rec := mustRecord(t, nonMember, yaml)
		if v := ruleGateOnly(rec, gateOnly); !hasKind(v, LintKindGateOnlyUnexpected) {
			t.Errorf("issues = %+v, want LintKindGateOnlyUnexpected", v)
		}
	})
}

// TestRuleArt11 proves the Art.11 TEN-ticket restriction both ways using
// the exact fixture 06-FORGE-SPEC.md §5 rule 25 names: AR/S-85.T5's
// Article-11 AC is required, never flagged as an unauthorized emission.
func TestRuleArt11(t *testing.T) {
	member := canonicalIDLoc{"AR", 9, 85, 5}.id() // AR/S-85.T5
	nonMember := "P1-E14-W3-S28-T1"
	art11 := idSet(art11Locs)
	// Appending after the DoD line (not before) keeps the new bullet
	// inside acceptance_criteria regardless of what follows in the doc.
	dodLine := `  - "checks green in CI; error-path tests present; -race clean; no Article-1 stubs; docs_updates landed; CR by a different agent; journal written"` + "\n"
	withArt11 := func(id string) string {
		return strings.Replace(cleanTicketYAML(id), dodLine, dodLine+"  - \"Art.11: hardening clause\"\n", 1)
	}

	t.Run("member missing the clause fires missing", func(t *testing.T) {
		rec := mustRecord(t, member, cleanTicketYAML(member))
		if v := ruleArt11(rec, art11); !hasKind(v, LintKindArt11Missing) {
			t.Errorf("issues = %+v, want LintKindArt11Missing", v)
		}
	})
	t.Run("member carrying the clause passes, never flagged unauthorized", func(t *testing.T) {
		rec := mustRecord(t, member, withArt11(member))
		v := ruleArt11(rec, art11)
		if len(v) != 0 {
			t.Errorf("issues = %+v, want none (Art.11 required, not unauthorized, for AR/S-85.T5)", v)
		}
	})
	t.Run("non-member carrying the clause fires unauthorized", func(t *testing.T) {
		rec := mustRecord(t, nonMember, withArt11(nonMember))
		if v := ruleArt11(rec, art11); !hasKind(v, LintKindArt11Unauthorized) {
			t.Errorf("issues = %+v, want LintKindArt11Unauthorized", v)
		}
	})
}

func TestRuleSubtickets(t *testing.T) {
	parent := "P1-E14-W3-S28-T1"
	childA := "P1-E14-W3-S28-T2"
	childB := "P1-E14-W3-S28-T3"

	t.Run("dangling subticket id fires", func(t *testing.T) {
		yaml := strings.Replace(cleanTicketYAML(parent), "journals: true\n", "journals: true\nsubtickets:\n  - "+childA+"\n", 1)
		rec := mustRecord(t, parent, yaml)
		byID := map[string]TicketRecord{}
		if v := ruleSubtickets(rec, byID); !hasKind(v, LintKindSubticketDangling) {
			t.Errorf("issues = %+v, want LintKindSubticketDangling", v)
		}
	})

	t.Run("overlapping files_scope between subtickets fires", func(t *testing.T) {
		yaml := strings.Replace(cleanTicketYAML(parent), "journals: true\n",
			"journals: true\nsubtickets:\n  - "+childA+"\n  - "+childB+"\n", 1)
		rec := mustRecord(t, parent, yaml)
		a := mustRecord(t, childA, cleanTicketYAML(childA)) // both use plugins/pbd/x.go
		b := mustRecord(t, childB, cleanTicketYAML(childB))
		byID := map[string]TicketRecord{childA: a, childB: b}
		if v := ruleSubtickets(rec, byID); !hasKind(v, LintKindSubticketOverlap) {
			t.Errorf("issues = %+v, want LintKindSubticketOverlap", v)
		}
	})

	t.Run("disjoint files_scope between subtickets passes", func(t *testing.T) {
		yaml := strings.Replace(cleanTicketYAML(parent), "journals: true\n",
			"journals: true\nsubtickets:\n  - "+childA+"\n  - "+childB+"\n", 1)
		rec := mustRecord(t, parent, yaml)
		a := mustRecord(t, childA, cleanTicketYAML(childA))
		bYAML := strings.Replace(cleanTicketYAML(childB), "plugins/pbd/x.go", "plugins/pbd/y.go", 1)
		b := mustRecord(t, childB, bYAML)
		byID := map[string]TicketRecord{childA: a, childB: b}
		if v := ruleSubtickets(rec, byID); len(v) != 0 {
			t.Errorf("issues = %+v, want none", v)
		}
	})
}

func hasKind(v []LintIssue, kind LintIssueKind) bool {
	for _, i := range v {
		if i.Kind == kind {
			return true
		}
	}
	return false
}
