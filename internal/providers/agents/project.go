// Purpose: ProjectEnvelope, the one conversion from a signed scope envelope
// to the driver-facing provider.SpawnEnvelope.
// Inputs: a signed envelope, the pinned controller public key, now.
// Outputs: a provider.SpawnEnvelope copied field for field from the verified
// envelope, or envelope.Verify's own refusal.
// Constraints: Verify runs first and its errors pass through unchanged; the
// projection copies and never derives except Digest = envelope.Digest of the
// verified envelope; slices are cloned so the result never aliases the input.
// SPORT: internal.providers.agents/ADD (P1-AGT-02, contract:scope-envelope use).

package agents

import (
	"crypto/ed25519"
	"slices"
	"time"

	"github.com/acamarata/cascade/pkg/envelope"
	"github.com/acamarata/cascade/pkg/provider"
)

// ProjectEnvelope verifies s under pinned at now and returns its projection.
func ProjectEnvelope(s envelope.Signed, pinned ed25519.PublicKey, now time.Time) (provider.SpawnEnvelope, error) {
	env, err := envelope.Verify(s, pinned, now)
	if err != nil {
		return provider.SpawnEnvelope{}, err
	}
	digest, err := envelope.Digest(env)
	if err != nil {
		return provider.SpawnEnvelope{}, err
	}
	return provider.SpawnEnvelope{
		Digest:      digest,
		Principal:   env.Principal,
		Roots:       slices.Clone(env.Roots),
		Tools:       slices.Clone(env.Tools),
		Commands:    slices.Clone(env.Commands),
		Egress:      slices.Clone(env.Egress),
		Sensitivity: env.Sensitivity,
		ExpiresAt:   env.ExpiresAt,
	}, nil
}
