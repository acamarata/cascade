package crunch

import "testing"

func TestA1ManageQuota(t *testing.T) {
	s := Scheduler{}
	if !s.Allow(RoleA1, ActionManage, 0) {
		t.Error("A1 manage quota should be allowed")
	}
}

func TestT0ManageQuota(t *testing.T) {
	s := Scheduler{}
	if !s.Allow(RoleT0, ActionManage, 0) {
		t.Error("T0 manage quota should be allowed")
	}
}

func TestA3ExecuteFullUtilization(t *testing.T) {
	s := Scheduler{}
	if !s.Allow(RoleA3, ActionExecute, 100) {
		t.Error("A3 execute at 100% utilization should be allowed")
	}
}

func TestA2AllowFullUtilization(t *testing.T) {
	s := Scheduler{}
	if !s.Allow(RoleA2, ActionExecute, 100) {
		t.Error("A2 at 100% utilization should be allowed")
	}
}

func TestA4CapAtFiftyPercent(t *testing.T) {
	s := Scheduler{}
	if !s.Allow(RoleA4, ActionExecute, 50) {
		t.Error("A4 at 50% utilization should be allowed")
	}
}

func TestA4RejectOverFiftyPercent(t *testing.T) {
	s := Scheduler{}
	if s.Allow(RoleA4, ActionExecute, 51) {
		t.Error("A4 at 51% utilization should be denied")
	}
}

func TestUnknownRoleDenied(t *testing.T) {
	s := Scheduler{}
	if s.Allow("X9", ActionExecute, 10) {
		t.Error("Unknown role X9 should be denied")
	}
}
