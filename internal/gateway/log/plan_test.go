package log

import (
	"testing"
)

func TestSelectGatewayTeamsPlan(t *testing.T) {
	u1 := "user1"
	err := SelectPlan(u1, "gateway-teams")
	if err != nil {
		t.Fatalf("SelectPlan(user1, \"gateway-teams\") returned error: %v", err)
	}
	// Verify the plan was set
	if !Allowed(u1, "team-seats") {
		t.Error("Expected user1 on gateway-teams plan to have team-seats feature")
	}
}

func TestGatewayTeamsAllowsPlanFeature(t *testing.T) {
	u1 := "user1"
	_ = SelectPlan(u1, "gateway-teams")
	if !Allowed(u1, "team-seats") {
		t.Error("Expected Allowed(user1, \"team-seats\") to be true for gateway-teams plan")
	}
}

func TestGatewayTeamsDeniesOtherFeature(t *testing.T) {
	u1 := "user1"
	_ = SelectPlan(u1, "gateway-teams")
	if Allowed(u1, "enterprise-sso") {
		t.Error("Expected Allowed(user1, \"enterprise-sso\") to be false for gateway-teams plan")
	}
}

func TestSelectUnknownPlanFails(t *testing.T) {
	u1 := "user1"
	// First set a valid plan
	_ = SelectPlan(u1, "gateway-teams")

	// Try to set an unknown plan
	err := SelectPlan(u1, "nope")
	if err == nil {
		t.Fatal("SelectPlan(user1, \"nope\") should return an error for unknown plan")
	}

	// Verify the prior plan is unchanged
	if !Allowed(u1, "team-seats") {
		t.Error("Expected user1 to still have gateway-teams plan features after failed SelectPlan")
	}
}
