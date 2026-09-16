package nodes

import (
	"strings"
	"time"
)

// Purpose (this file): the credential rules of a dispatch (§D-11, 06 §5.22,
//
//	R-21.222) — which credentials may reach a node at all, what a
//	per-dispatch token is bound to, and where it may travel.
//
// Inputs: a lane's credential requirement and the dispatch's identity.
// Outputs: either a scoped, short-lived token grant, or a refusal.
// Constraints: the two rules here are unconditional and not configurable.
//
//	STATIC API KEYS ARE NEVER SHIPPED. A static key on a node is a
//	long-lived secret on another machine's disk for as long as that machine
//	exists; lanes needing one relay through the controller's Conductor
//	instead. A per-dispatch token is the exception precisely because it
//	expires, is bound to one job on one node for one audience, and dies
//	with the process holding it.
//
//	A token grant carries NO secret material. It names a vault key and the
//	binding; the value itself is delivered over the mutually authenticated
//	control channel to the node's in-memory broker and is never written to
//	git, journals, task payloads, argv, environment or any persisted store.
//	That is why this file can be read, logged and tested freely: there is
//	nothing secret in the type it produces.
//
// SPORT: internal/nodes:dispatch-creds (ADD) — P1-E17-W4-S37-T2.

// MaxTokenLifetime is the ceiling on a per-dispatch token (R-21.222).
//
// One hour is the contract's figure, and it is a CEILING rather than a
// default: a grant asking for longer is clamped rather than refused,
// because the safe outcome of asking for too much lifetime is getting less
// of it, not failing the dispatch.
const MaxTokenLifetime = time.Hour

// CredentialKind is how a lane's credential is supplied.
type CredentialKind string

const (
	// CredentialStatic is a long-lived API key. Relay-only: never shipped.
	CredentialStatic CredentialKind = "static"
	// CredentialScoped is an OAuth/short-lived-capable credential that can
	// be minted per dispatch.
	CredentialScoped CredentialKind = "scoped"
	// CredentialNone is a lane needing no credential at all.
	CredentialNone CredentialKind = "none"
)

// TokenGrant is a per-dispatch scoped token's BINDING — never its value.
//
// Every field narrows what the token can do. A grant missing any of them
// would be a token that outlives, out-scopes or out-travels the dispatch it
// was minted for, which is the thing §D-11 exists to prevent.
type TokenGrant struct {
	// JobID is the job this token may act for.
	JobID string
	// NodeID is the single node it may be delivered to.
	NodeID string
	// Audience is the provider it may authenticate against.
	Audience string
	// Verbs are the operations it permits.
	Verbs []string
	// VaultKey names where the value lives. The VALUE is never here.
	VaultKey string
	// ExpiresAt is when it stops working.
	ExpiresAt time.Time
}

// CredentialPlan is what a dispatch may carry for one lane.
type CredentialPlan struct {
	// Kind is how the credential is supplied.
	Kind CredentialKind
	// Grant is set only for CredentialScoped.
	Grant *TokenGrant
	// RelayThroughController is true when the lane's calls must be made by
	// the controller on the node's behalf, which is every static-key lane.
	RelayThroughController bool
}

// PlanCredentials decides what a dispatch may carry for a lane.
//
// A static-key lane is not an error: it is a lane that relays. The refusal
// (ErrStaticKeyOnDispatch) is for the separate case of a payload that
// already contains a static key, which AssertNoStaticKey catches — this
// function's job is to make sure that case never arises by never planning
// one onto the wire in the first place.
func PlanCredentials(kind CredentialKind, jobID, nodeID, audience, vaultKey string,
	verbs []string, now time.Time, lifetime time.Duration,
) (CredentialPlan, error) {
	switch kind {
	case CredentialNone:
		return CredentialPlan{Kind: CredentialNone}, nil
	case CredentialStatic:
		return CredentialPlan{Kind: CredentialStatic, RelayThroughController: true}, nil
	case CredentialScoped:
		grant, err := NewTokenGrant(jobID, nodeID, audience, vaultKey, verbs, now, lifetime)
		if err != nil {
			return CredentialPlan{}, err
		}
		return CredentialPlan{Kind: CredentialScoped, Grant: &grant}, nil
	default:
		// Fail-closed: an unrecognized credential kind relays rather than
		// guessing that it is safe to ship.
		return CredentialPlan{Kind: CredentialStatic, RelayThroughController: true}, nil
	}
}

// NewTokenGrant builds a bound, short-lived grant.
func NewTokenGrant(jobID, nodeID, audience, vaultKey string, verbs []string,
	now time.Time, lifetime time.Duration,
) (TokenGrant, error) {
	for name, value := range map[string]string{
		"job": jobID, "node": nodeID, "audience": audience, "vault key": vaultKey,
	} {
		if strings.TrimSpace(value) == "" {
			return TokenGrant{}, errUnboundGrant(name)
		}
	}
	if len(verbs) == 0 {
		return TokenGrant{}, errUnboundGrant("verbs")
	}
	if lifetime <= 0 || lifetime > MaxTokenLifetime {
		lifetime = MaxTokenLifetime
	}
	return TokenGrant{
		JobID:     jobID,
		NodeID:    nodeID,
		Audience:  audience,
		Verbs:     append([]string(nil), verbs...),
		VaultKey:  vaultKey,
		ExpiresAt: now.Add(lifetime),
	}, nil
}

// Valid reports whether the grant still authorizes anything at now.
func (g TokenGrant) Valid(now time.Time) bool { return now.Before(g.ExpiresAt) }

// Permits reports whether the grant covers one call.
func (g TokenGrant) Permits(nodeID, audience, verb string, now time.Time) bool {
	if !g.Valid(now) || g.NodeID != nodeID || g.Audience != audience {
		return false
	}
	for _, allowed := range g.Verbs {
		if allowed == verb {
			return true
		}
	}
	return false
}

// staticKeyPrefixes are the recognizable shapes of long-lived provider API
// keys. The list is a HEURISTIC and is documented as one: it catches the
// common formats a payload might carry by accident, and it is not the
// security boundary. The boundary is PlanCredentials, which never plans a
// static key onto a dispatch at all.
//
// A heuristic in front of a real boundary is worth having anyway: it turns
// "a key leaked into a payload by some path nobody anticipated" from a
// silent shipment into a refusal.
var staticKeyPrefixes = []string{
	"sk-",     // OpenAI and compatible
	"sk-ant-", // Anthropic
	"ghp_",    // GitHub personal access token
	"gho_",    // GitHub OAuth token
	"github_pat_",
	"xoxb-", // Slack bot
	"AIza",  // Google API key
	"AKIA",  // AWS access key id
}

// AssertNoStaticKey refuses a dispatch payload that carries something
// shaped like a long-lived API key.
//
// This is defense in depth behind PlanCredentials, not a substitute for
// it: a key that reaches a node is a secret on another machine's disk for
// as long as that machine exists, so the payload is checked as well as the
// plan. §D-11 / 06 §5.22.
func AssertNoStaticKey(dispatchID string, payload []byte) error {
	haystack := string(payload)
	for _, prefix := range staticKeyPrefixes {
		if strings.Contains(haystack, prefix) {
			return ErrStaticKeyOnDispatch(dispatchID)
		}
	}
	return nil
}
