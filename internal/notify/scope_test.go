package notify

import "testing"

func session(id string, scopeIDs ...string) CandidateSession {
	m := make(map[string]bool, len(scopeIDs))
	for _, s := range scopeIDs {
		m[s] = true
	}
	return CandidateSession{SessionID: id, ScopeIDs: m}
}

func TestScopeDeliveryPredicateScoped(t *testing.T) {
	sess := session("s1", "project-1")
	n := Notification{Class: ClassScoped, TargetScope: "project-1"}
	if !ScopeDeliveryPredicate(sess, n) {
		t.Fatal("expected delivery: target scope is in the session's candidate set")
	}
}

func TestScopeDeliveryPredicateScopedFallsBackToOrigin(t *testing.T) {
	sess := session("s1", "project-1")
	n := Notification{Class: ClassScoped, OriginScope: "project-1"}
	if !ScopeDeliveryPredicate(sess, n) {
		t.Fatal("expected delivery via OriginScope fallback")
	}
}

// TestScopeDeliveryPredicateLeak is the ticket-mandated leak test: a
// Notification scoped to Project3 must never be delivered to a session
// scoped only to Project1.
func TestScopeDeliveryPredicateLeak(t *testing.T) {
	project1Session := session("s-project1", "project-1")
	n := Notification{Class: ClassScoped, TargetScope: "project-3"}
	if ScopeDeliveryPredicate(project1Session, n) {
		t.Fatal("leak: a Project3-scoped notification was delivered to a Project1-scoped session")
	}
}

func TestScopeDeliveryPredicateAddressed(t *testing.T) {
	sess := session("s1")
	matched := Notification{Class: ClassAddressed, TargetSession: "s1"}
	if !ScopeDeliveryPredicate(sess, matched) {
		t.Fatal("expected delivery: addressed to this exact session")
	}
	other := Notification{Class: ClassAddressed, TargetSession: "s2"}
	if ScopeDeliveryPredicate(sess, other) {
		t.Fatal("addressed notification delivered to the wrong session")
	}
}

func TestScopeDeliveryPredicateGlobalCritical(t *testing.T) {
	sess := session("s1")
	n := Notification{Class: ClassGlobalCritical}
	if !ScopeDeliveryPredicate(sess, n) {
		t.Fatal("expected global-critical to always deliver")
	}
}

func TestScopeDeliveryPredicateUnknownClassFailsClosed(t *testing.T) {
	sess := session("s1", "project-1")
	n := Notification{Class: Class("bogus"), TargetScope: "project-1", OriginScope: "project-1"}
	// Resolve() maps an unknown Class to Scoped, so this is a legitimate
	// scoped delivery once resolved -- verifying Resolve is applied.
	if !ScopeDeliveryPredicate(sess, n) {
		t.Fatal("expected unknown Class to resolve to Scoped and deliver when scope matches")
	}
}

func TestScopeDeliveryPredicateUnresolvableScopeWithheld(t *testing.T) {
	sess := session("s1", "project-1")
	n := Notification{Class: ClassScoped} // no OriginScope, no TargetScope
	if ScopeDeliveryPredicate(sess, n) {
		t.Fatal("expected an unresolvable scoped notification to be withheld from every session")
	}
}

func TestScopeDeliveryPredicateAddressedEmptyTargetWithheld(t *testing.T) {
	sess := session("s1")
	n := Notification{Class: ClassAddressed}
	if ScopeDeliveryPredicate(sess, n) {
		t.Fatal("expected an addressed notification with no target session to be withheld")
	}
}
