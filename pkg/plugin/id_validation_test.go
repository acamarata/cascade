// Purpose: table-driven coverage for the id-format rule (R-14.127) and the
// plugin.__host__.* reserved-namespace rejection (R-14.127b), for both the
// id field and provides.domains[].Name. New file rather than an extension
// of validate_rules_test.go — that file already sits close to Art.10.3's
// 300-line cap and this is a distinct CR-remediation concern (R-14.117
// authorizes package-local file splits for exactly this reason).
// Constraints: no network calls (Art.7.2); no writes at all; tests build
// Manifest values directly via validManifest (manifest_test.go) rather than
// through TOML text, so non-ASCII/quote-heavy ids need no TOML escaping.
package plugin_test

import (
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
)

// hasErrCode reports whether errs contains a finding for field with the
// given ErrCode. Shared by this file and host_version_test.go.
func hasErrCode(errs []plugin.ValidationError, field string, code plugin.ErrCode) bool {
	for _, e := range errs {
		if e.Field == field && e.Kind == code {
			return true
		}
	}
	return false
}

// idFormatCase is one row of the id-format test table.
type idFormatCase struct {
	name    string
	id      string
	wantErr bool
}

// idFormatCases enumerates every degenerate id CR found validating clean
// today (R-14.127: "UPPER CASE", "../../etc/passwd", "plugin.__host__.evil",
// "a b", "日本語"), plus the boundary cases the ticket names explicitly:
// empty, leading digit, leading hyphen, trailing hyphen, and a valid id.
// "trailing hyphen" is a POSITIVE case, not degenerate: idPatternRe is
// exactly [a-z][a-z0-9-]* as documented on Manifest.ID, and a hyphen is a
// legal character anywhere after the first, including at the end — this
// case exists to prove the enforced pattern matches the documented one,
// not to assert a rejection the pattern was never specified to produce.
func idFormatCases() []idFormatCase {
	return []idFormatCase{
		{"upper case", "UPPER CASE", true},
		{"path traversal", "../../etc/passwd", true},
		{"reserved host namespace chars", "plugin.__host__.evil", true},
		{"space separated", "a b", true},
		{"non-ascii", "日本語", true},
		{"empty", "", true},
		{"leading digit", "1abc", true},
		{"leading hyphen", "-abc", true},
		{"trailing hyphen", "abc-", false},
		{"valid id", "valid-plugin-id", false},
	}
}

func TestValidate_IDFormat(t *testing.T) {
	for _, tt := range idFormatCases() {
		t.Run(tt.name, func(t *testing.T) {
			m := validManifest()
			m.ID = tt.id
			errs := plugin.Validate(m)
			got := hasErrCode(errs, "id", plugin.ErrCodeRequiredField)
			if got != tt.wantErr {
				t.Errorf("Validate(id=%q): id ErrCodeRequiredField present = %v, want %v (errs=%+v)", tt.id, got, tt.wantErr, errs)
			}
		})
	}
}

// TestValidate_ReservedHostNamespace covers R-14.127b for both the id field
// and provides.domains[].Name, plus a positive control proving a
// non-reserved domain name is accepted.
func TestValidate_ReservedHostNamespace(t *testing.T) {
	t.Run("id exact match", func(t *testing.T) {
		m := validManifest()
		m.ID = "plugin.__host__"
		errs := plugin.Validate(m)
		if !hasErrCode(errs, "id", plugin.ErrCodeCommandNameCollision) {
			t.Errorf("Validate(id=%q) = %+v, want id ErrCodeCommandNameCollision", m.ID, errs)
		}
	})

	t.Run("id nested under namespace", func(t *testing.T) {
		m := validManifest()
		m.ID = "plugin.__host__.evil"
		errs := plugin.Validate(m)
		// This id also fails idPatternRe (dots/underscores aren't legal
		// id characters) — Validate never short-circuits, so BOTH
		// findings must be present.
		if !hasErrCode(errs, "id", plugin.ErrCodeRequiredField) {
			t.Errorf("Validate(id=%q) = %+v, want id ErrCodeRequiredField too", m.ID, errs)
		}
		if !hasErrCode(errs, "id", plugin.ErrCodeCommandNameCollision) {
			t.Errorf("Validate(id=%q) = %+v, want id ErrCodeCommandNameCollision", m.ID, errs)
		}
	})

	t.Run("domain name", func(t *testing.T) {
		m := validManifest()
		m.Provides.Domains = []plugin.DomainSpec{{Name: "plugin.__host__.metadata", Description: "x"}}
		errs := plugin.Validate(m)
		if !hasErrCode(errs, "provides.domains[0].name", plugin.ErrCodeCommandNameCollision) {
			t.Errorf("Validate(domain=%q) = %+v, want domain ErrCodeCommandNameCollision", m.Provides.Domains[0].Name, errs)
		}
	})

	t.Run("non-reserved domain name is accepted", func(t *testing.T) {
		m := validManifest()
		m.Provides.Domains = []plugin.DomainSpec{{Name: "my-domain", Description: "x"}}
		if errs := plugin.Validate(m); len(errs) != 0 {
			t.Errorf("Validate(domain=my-domain) = %+v, want no errors", errs)
		}
	})
}
