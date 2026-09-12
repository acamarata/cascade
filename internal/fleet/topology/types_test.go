package topology

import "testing"

func TestIdentifierTypesRejectEmpty(t *testing.T) {
	if AccountID("").Valid() {
		t.Error("AccountID(\"\") should be invalid")
	}
	if !AccountID("a1").Valid() {
		t.Error("AccountID(\"a1\") should be valid")
	}
	if CredentialID("").Valid() {
		t.Error("CredentialID(\"\") should be invalid")
	}
	if QuotaDomainID("").Valid() {
		t.Error("QuotaDomainID(\"\") should be invalid")
	}
	var d DomainID
	if d.Valid() {
		t.Error("DomainID(\"\") (alias of QuotaDomainID) should be invalid")
	}
	if RuntimeProfileID("").Valid() {
		t.Error("RuntimeProfileID(\"\") should be invalid")
	}
	if LaneID("").Valid() {
		t.Error("LaneID(\"\") should be invalid")
	}
}

func TestVaultKeyRefString(t *testing.T) {
	ref := VaultKeyRef("vault://acct/key-1")
	if ref.String() != "vault://acct/key-1" {
		t.Errorf("String() = %q, want the ref name unchanged", ref.String())
	}
}

// TestDomainIDIsQuotaDomainIDAlias proves DomainID is usable anywhere a
// QuotaDomainID is expected (R-21.24's own "alias" wording), not merely a
// same-named distinct type.
func TestDomainIDIsQuotaDomainIDAlias(t *testing.T) {
	var q QuotaDomainID = "acct:api"
	d := q
	if d != q {
		t.Fatalf("DomainID(%q) != QuotaDomainID(%q); alias assignment should be direct", d, q)
	}
	cred := Credential{QuotaDomainRef: q}
	if cred.QuotaDomainRef != d {
		t.Error("Credential.QuotaDomainRef (typed DomainID) should accept a QuotaDomainID value directly")
	}
}
