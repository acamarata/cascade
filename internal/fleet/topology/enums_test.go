package topology

import "testing"

func TestRoleValid(t *testing.T) {
	members := []Role{
		RoleExecutive, RolePlanner, RoleArchitect, RoleImplementationLead, RoleCoder,
		RoleFastCoder, RoleOperator, RoleResearcher, RoleScout, RoleDistiller,
		RoleLongContext, RoleTester, RoleReducer, RoleCritic, RoleAdversary,
		RoleRecovery, RoleAutonomousSubproject, RoleFinalAcceptance,
	}
	if len(members) != 18 {
		t.Fatalf("expected 18 Role members, got %d", len(members))
	}
	for _, m := range members {
		if !m.Valid() {
			t.Errorf("Role %q should be valid", m)
		}
	}
	if Role("").Valid() {
		t.Error("zero-value Role should be invalid")
	}
	if Role("bogus").Valid() {
		t.Error("unrecognised Role should be invalid")
	}
}

func TestBillingKindValid(t *testing.T) {
	for _, k := range []BillingKind{BillingSubscription, BillingAPI, BillingFree, BillingPool} {
		if !k.Valid() {
			t.Errorf("BillingKind %q should be valid", k)
		}
	}
	if BillingKind("").Valid() || BillingKind("bogus").Valid() {
		t.Error("empty/unknown BillingKind should be invalid")
	}
}

func TestAccountRoleValid(t *testing.T) {
	for _, r := range []AccountRole{AccountRoleExecutive, AccountRoleWorkforce, AccountRoleSpecialist} {
		if !r.Valid() {
			t.Errorf("AccountRole %q should be valid", r)
		}
	}
	if AccountRole("").Valid() {
		t.Error("zero-value AccountRole should be invalid")
	}
}

func TestCredentialHealthValid(t *testing.T) {
	for _, h := range []CredentialHealth{CredentialOK, CredentialQuarantined} {
		if !h.Valid() {
			t.Errorf("CredentialHealth %q should be valid", h)
		}
	}
	if CredentialHealth("").Valid() {
		t.Error("zero-value CredentialHealth should be invalid")
	}
}

func TestQuotaDomainKindValid(t *testing.T) {
	for _, k := range []QuotaDomainKind{QuotaDomainAPIProject, QuotaDomainSubscriptionWindow, QuotaDomainSharedPool} {
		if !k.Valid() {
			t.Errorf("QuotaDomainKind %q should be valid", k)
		}
	}
	if QuotaDomainKind("").Valid() {
		t.Error("zero-value QuotaDomainKind should be invalid")
	}
}

func TestBillingTierValid(t *testing.T) {
	for _, tier := range []BillingTier{BillingTierFree, BillingTierPaid, BillingTierDiscover} {
		if !tier.Valid() {
			t.Errorf("BillingTier %q should be valid", tier)
		}
	}
	if BillingTier("").Valid() {
		t.Error("zero-value BillingTier should be invalid")
	}
}

func TestRuntimeKindValid(t *testing.T) {
	members := []RuntimeKind{RuntimeClaudeCLI, RuntimeCodex, RuntimeAntigravity, RuntimeOpenCode, RuntimeAPI, RuntimeOllama}
	if len(members) != 6 {
		t.Fatalf("expected 6 RuntimeKind members, got %d", len(members))
	}
	for _, k := range members {
		if !k.Valid() {
			t.Errorf("RuntimeKind %q should be valid", k)
		}
	}
	if RuntimeKind("").Valid() {
		t.Error("zero-value RuntimeKind should be invalid")
	}
}

func TestEffortValid(t *testing.T) {
	for _, e := range []Effort{EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax, EffortNA} {
		if !e.Valid() {
			t.Errorf("Effort %q should be valid", e)
		}
	}
	if Effort("").Valid() {
		t.Error("zero-value Effort should be invalid")
	}
}

func TestInteractionClassValid(t *testing.T) {
	for _, c := range []InteractionClass{InteractionInteractive, InteractionBatch} {
		if !c.Valid() {
			t.Errorf("InteractionClass %q should be valid", c)
		}
	}
	if InteractionClass("").Valid() {
		t.Error("zero-value InteractionClass should be invalid")
	}
}

func TestLaneHealthValid(t *testing.T) {
	members := []LaneHealth{LaneHealthAvailable, LaneHealthConstrained, LaneHealthExhausted,
		LaneHealthAuthRequired, LaneHealthQuarantined, LaneHealthUnknown}
	for _, h := range members {
		if !h.Valid() {
			t.Errorf("LaneHealth %q should be valid", h)
		}
	}
	if LaneHealth("").Valid() {
		t.Error("zero-value LaneHealth should be invalid")
	}
}

func TestEncodeDecodeRolesRoundTrip(t *testing.T) {
	roles := []Role{RoleCoder, RoleCritic, RoleTester}
	encoded, err := encodeRoles(roles)
	if err != nil {
		t.Fatalf("encodeRoles: %v", err)
	}
	decoded, err := decodeRoles(encoded)
	if err != nil {
		t.Fatalf("decodeRoles: %v", err)
	}
	if len(decoded) != len(roles) {
		t.Fatalf("round trip length mismatch: got %d, want %d", len(decoded), len(roles))
	}
	for i, r := range roles {
		if decoded[i] != r {
			t.Errorf("decoded[%d] = %q, want %q", i, decoded[i], r)
		}
	}
}

func TestDecodeRolesMalformedFails(t *testing.T) {
	if _, err := decodeRoles("not json"); err == nil {
		t.Fatal("decodeRoles(malformed) should have failed")
	}
}

func TestEncodeRolesEmpty(t *testing.T) {
	encoded, err := encodeRoles(nil)
	if err != nil {
		t.Fatalf("encodeRoles(nil): %v", err)
	}
	if encoded != "[]" {
		t.Errorf("encodeRoles(nil) = %q, want \"[]\"", encoded)
	}
}

func TestEveryEnumStringMethod(t *testing.T) {
	cases := []struct {
		name string
		got  string
	}{
		{"BillingKind", BillingAPI.String()},
		{"AccountRole", AccountRoleWorkforce.String()},
		{"CredentialHealth", CredentialOK.String()},
		{"QuotaDomainKind", QuotaDomainAPIProject.String()},
		{"BillingTier", BillingTierDiscover.String()},
		{"RuntimeKind", RuntimeAPI.String()},
		{"Effort", EffortHigh.String()},
		{"InteractionClass", InteractionInteractive.String()},
		{"LaneHealth", LaneHealthAvailable.String()},
	}
	for _, c := range cases {
		if c.got == "" {
			t.Errorf("%s.String() should not be empty", c.name)
		}
	}
}
