package inboxenv

import "github.com/acamarata/cascade/pkg/provider"

// ModelCeiling is the sensitivity ceiling for models.
const ModelCeiling = provider.SensitivityInternal

// ModelAdmits reports whether t is admitted by the model ceiling.
// It returns true iff JoinSensitivity(t, ModelCeiling) == ModelCeiling.
// Restricted, local-only and any invalid tier return false.
func ModelAdmits(t provider.SensitivityTier) bool {
	if !t.Valid() {
		return false
	}
	return provider.JoinSensitivity(t, ModelCeiling) == ModelCeiling
}
