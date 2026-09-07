// Purpose: the intake package's data shapes (P1-E10-W3-S20-T1): the closed
//   DriverKind/AuthType enumerations, the VaultKeyRef type that can never
//   carry a credential value, the ProviderRecord this command writes, and
//   the Registry seam a future S-20.T2 registry domain implements. S-20.T2
//   has not landed as of this ticket (internal/providers/registry does not
//   exist in the tree); this interface is the integration point it will
//   satisfy, and MemoryRegistry is the interim, in-process implementation
//   that makes this ticket's idempotency contract real and testable today.
// Inputs: none at this layer - these are contracts and one small store.
// Outputs: none.
// Constraints: AuthRef is a VaultKeyRef (a name), never a secret value - no
//   field anywhere in this file can hold one, so "a credential leaked into
//   the record" is a compile-time impossibility rather than a review
//   finding. Every enum's zero value is invalid or the honest "unknown"
//   member, never a permissive default (Art.1/R-14.88).
// SPORT: provider.intake/ADD (P1-E10-W3-S20-T1).

package intake

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// DriverKind names the driver family the shape probe (or a non-interactive
// directive) resolved. The five members mirror 08-INIT-CONFIG-SPEC.md §2's
// [[providers]] kind enum exactly - no sixth member, no free-form string.
type DriverKind string

const (
	// DriverAnthropic matches the anthropic-compat /v1/models shape.
	DriverAnthropic DriverKind = "anthropic"
	// DriverOpenAICompat matches the openai-compat /v1/models shape
	// (OpenAI itself, zai, Kimi/Moonshot, DeepSeek - one protocol).
	DriverOpenAICompat DriverKind = "openai-compat"
	// DriverGemini matches the Gemini models.list shape.
	DriverGemini DriverKind = "gemini"
	// DriverOllama is a local Ollama server; never shape-probed (no
	// vendor credential to try), selected only by an explicit directive.
	DriverOllama DriverKind = "ollama"
	// DriverLocalLLM is a local, non-Ollama OpenAI-compatible server.
	DriverLocalLLM DriverKind = "localllm"
)

// Valid reports whether k is one of the five declared members.
func (k DriverKind) Valid() bool {
	switch k {
	case DriverAnthropic, DriverOpenAICompat, DriverGemini, DriverOllama, DriverLocalLLM:
		return true
	}
	return false
}

// AuthType names how a provider record's credential is held. Exactly two
// members: 08-INIT-CONFIG-SPEC.md §2 defines no third.
type AuthType string

const (
	// AuthKey is a static API key resolved through the vault broker.
	AuthKey AuthType = "key"
	// AuthOAuth is a broker-managed OAuth grant.
	AuthOAuth AuthType = "oauth"
)

// Valid reports whether a is AuthKey or AuthOAuth.
func (a AuthType) Valid() bool { return a == AuthKey || a == AuthOAuth }

// VaultKeyRef is a vault broker key NAME, never a credential value. It is
// declared as a distinct type (rather than a plain string field on
// ProviderRecord) so a reviewer - and a compiler, for any function that
// takes a VaultKeyRef where the caller only has a string - sees the
// distinction between "a name" and "a value" at every call site.
type VaultKeyRef string

// String returns the ref's name. It is safe to log: a VaultKeyRef this
// package constructs is always a name the intake command itself minted
// (see keyRefFor), never operator-supplied text that might itself look
// like a credential.
func (r VaultKeyRef) String() string { return string(r) }

// keyRefFor mints the deterministic vault key name a provider's credential
// is stored under: "provider.<name>.key" or "provider.<name>.oauth.access"
// / "...oauth.refresh". One naming rule, so intake and a future re-verify
// or removal path never disagree about where a credential lives.
func keyRefFor(providerName, suffix string) VaultKeyRef {
	return VaultKeyRef("provider." + providerName + "." + suffix)
}

// ProviderRecord is the registry row this command upserts. Its shape
// mirrors the S-20.T2 schema (04-PEWS-PLAN-W1-W3.md §Epic J S-20.T2); this
// package owns only the fields intake itself populates.
type ProviderRecord struct {
	// Name is the unique provider name; the registry's primary key.
	Name string `json:"provider_name"`
	// Driver is the resolved driver kind.
	Driver DriverKind `json:"driver_kind"`
	// BaseURL is the resolved API root; empty means the driver default.
	BaseURL string `json:"base_url,omitempty"`
	// Auth is the resolved auth type.
	Auth AuthType `json:"auth_type"`
	// AuthRef is the vault-key reference the credential lives under.
	// NEVER a credential value - see VaultKeyRef's own doc comment.
	AuthRef VaultKeyRef `json:"auth_ref"`
	// KnownModels is the model-enumeration result, updated on re-verify.
	KnownModels []string `json:"known_models"`
	// Capabilities is the R-14.88 tri-state probe result. Never a
	// compile-time constant for a hosted driver: every field starts
	// CapabilityUnknown and is set only by an actual probe outcome.
	Capabilities provider.Capabilities `json:"capabilities"`
	// CapabilitiesProbedAt is when Capabilities was last refreshed.
	CapabilitiesProbedAt time.Time `json:"capabilities_probed_at,omitzero"`
	// Pool is the pool_membership name, or "" for a standalone provider.
	Pool string `json:"pool_membership,omitempty"`
	// PoolIndex is the round-robin dispatch index within Pool.
	PoolIndex int `json:"pool_index,omitempty"`
	// VerifySkipped records that --no-verify (or verify=false) skipped
	// the live micro-verify for this record's most recent (re-)add.
	VerifySkipped bool `json:"verify_skipped,omitempty"`
	// CreatedAt/UpdatedAt are registry-managed, injected-clock timestamps.
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Registry is the write-capable seam this command upserts through. A
// future S-20.T2 registry domain implements it against durable storage;
// MemoryRegistry (below) is the interim, in-process implementation this
// ticket ships and tests against, per this file's header comment.
type Registry interface {
	// UpsertProvider inserts or updates by record.Name. It is the
	// idempotency point: a second call with the same Name updates the
	// existing row rather than creating a duplicate.
	UpsertProvider(ctx context.Context, record ProviderRecord) error
	// GetProvider returns the record named name, or a KindNotFound error.
	GetProvider(ctx context.Context, name string) (ProviderRecord, error)
	// ListPool returns every member of pool, sorted by provider name.
	ListPool(ctx context.Context, pool string) ([]ProviderRecord, error)
}

// MemoryRegistry is a concurrency-safe, in-process Registry. It is the
// interim store: durable, cross-process persistence is S-20.T2's registry
// domain (not yet landed - see this file's header). A test, and this
// ticket's own CLI wiring until S-20.T2 lands, uses this directly.
type MemoryRegistry struct {
	mu       sync.RWMutex
	byName   map[string]ProviderRecord
	poolNext map[string]int
}

// NewMemoryRegistry returns an empty registry.
func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{byName: map[string]ProviderRecord{}, poolNext: map[string]int{}}
}

// UpsertProvider implements Registry.
func (m *MemoryRegistry) UpsertProvider(_ context.Context, record ProviderRecord) error {
	if strings.TrimSpace(record.Name) == "" {
		return cascade.New(cascade.KindInvalidInput, "intake: a provider record needs a non-empty name")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byName[record.Name] = record
	return nil
}

// GetProvider implements Registry.
func (m *MemoryRegistry) GetProvider(_ context.Context, name string) (ProviderRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.byName[name]
	if !ok {
		return ProviderRecord{}, cascade.Newf(cascade.KindNotFound, "intake: no provider named %q", name)
	}
	return rec, nil
}

// ListPool implements Registry: every member whose Pool matches, sorted by
// provider name for deterministic dispatch order (04-PEWS-PLAN §S-20.T2).
func (m *MemoryRegistry) ListPool(_ context.Context, pool string) ([]ProviderRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []ProviderRecord
	for _, rec := range m.byName {
		if rec.Pool == pool {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// nextPoolIndex returns the next round-robin index for pool and advances
// it. Called only from core.go's join path, under the same lock discipline
// as UpsertProvider (core.go serialises pool joins through Deps.mu).
func (m *MemoryRegistry) nextPoolIndex(pool string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := m.poolNext[pool]
	m.poolNext[pool] = n + 1
	return n
}

// Clock abstracts the wall clock (Art.7.3: no bare time.Now). Declared
// locally, structurally identical to internal/runtime.Clock, so this
// package's own tests and internal/testkit's clock both satisfy it with
// zero adapters.
type Clock interface {
	Now() time.Time
}
