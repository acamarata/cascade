package log

import (
	"fmt"
	"sync"
)

// PlanFeatureMap defines the features available for each known plan.
var PlanFeatureMap = map[string]map[string]bool{
	"gateway-teams": {
		"team-seats": true,
	},
}

// knownPlans is a set of valid plan names.
var knownPlans = map[string]bool{
	"gateway-teams": true,
}

var (
	mu        sync.Mutex
	userPlans = make(map[string]string)
)

// SelectPlan records the plan for the given user if it is a known plan name.
// Returns an error if the plan is unknown, leaving the user's plan unchanged.
func SelectPlan(user string, plan string) error {
	if !knownPlans[plan] {
		return fmt.Errorf("unknown plan: %s", plan)
	}

	mu.Lock()
	defer mu.Unlock()

	userPlans[user] = plan
	return nil
}

// Allowed returns true if the given feature is included in the user's current plan.
// Returns false if the user has no plan or the feature is not in the plan's feature set.
func Allowed(user string, feature string) bool {
	mu.Lock()
	defer mu.Unlock()

	plan, ok := userPlans[user]
	if !ok {
		return false
	}

	features, ok := PlanFeatureMap[plan]
	if !ok {
		return false
	}

	return features[feature]
}
