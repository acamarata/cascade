// Purpose: the sync domain-class registry (P1-E17-W4-S38-T1) — a CLOSED
//   mapping over storage's twelve-domain cascade.db contract (R-14.5,
//   amended R-16.51/R-16.75), never a new SQLite domain of its own
//   (R-21.223). Each registered domain family carries a Class
//   (local-only | synced | server-primary) and a default record
//   SensitivityTier; a domain the registry does not list is local-only
//   and never syncs (R-16.56) — there is no default merge.
// Inputs: none at call time; the table is a compile-time constant.
// Outputs: Lookup(domain, subkind) -> (DomainClass, bool); PluginClass
//   resolves a plugin manifest's `storage.sync` declaration.
// Constraints: the vault (storage.DomainSecrets) is STRUCTURALLY absent
//   from this table — there is no entry to look up, so a caller that
//   tries always gets the local-only default, never a synced answer.
//   CONTRADICTION (05-PEWS-PLAN / ticket text vs tree): the contract
//   describes "the closed ten-domain storage contract"; storage/domains.go
//   has since been amended twice (R-16.51 adds `policy`, R-16.75 adds
//   `ci_results`) to a closed TWELVE. This registry maps over the twelve
//   domains the tree actually declares (storage.AllDomains), per
//   AGENT-BRIEF's "read the real code first and follow the tree" rule.
// SPORT: internal.sync.domains/ADDED (P1-E17-W4-S38-T1).

package sync

import (
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/storage"
)

// Class is one domain's sync eligibility, per 00-VISION §Sync's enum plus
// the plugin-manifest-only append class (O/S-32.T3's `storage.sync` key).
type Class string

const (
	// ClassLocalOnly never leaves the owning device. The zero value of a
	// lookup miss resolves here (R-16.56 — never a default merge).
	ClassLocalOnly Class = "local-only"
	// ClassSynced replicates bidirectionally between peers.
	ClassSynced Class = "synced"
	// ClassServerPrimary treats the server as the write authority; peers
	// replicate FROM it. Used for accounts (metadata only — static keys
	// never ship, Epic Q preamble §D-11).
	ClassServerPrimary Class = "server-primary"
	// ClassSyncedAppend is append-only synced, available only to
	// plugin-scoped domains via their manifest's `storage.sync` key.
	ClassSyncedAppend Class = "synced-append"
)

// Valid reports whether c is one of the four declared classes.
func (c Class) Valid() bool {
	switch c {
	case ClassLocalOnly, ClassSynced, ClassServerPrimary, ClassSyncedAppend:
		return true
	default:
		return false
	}
}

// DomainClass describes one syncable record family: the closed-set
// storage domain it is durably persisted under, a human-readable Subkind
// distinguishing record families sharing one domain (R-21.223's mapping:
// conversation -> context, registry/accounts -> config), the sync Class,
// and the default SensitivityTier a record of this family carries absent
// its own per-record override.
type DomainClass struct {
	Domain      storage.DomainID
	Subkind     string
	Class       Class
	Sensitivity egress.SensitivityTier
}

// registryKey identifies one DomainClass entry by its (Domain, Subkind)
// pair, since one storage domain can host more than one sync subkind
// (e.g. DomainConfig hosts "config", "registry", "accounts",
// "phase-state").
type registryKey struct {
	domain  storage.DomainID
	subkind string
}

// coreDomainTable is the closed, compile-time sync domain-class registry.
// R-21.223 mandates two mappings explicitly (conversation -> context,
// registry/accounts -> config); the remaining 00-VISION families
// (memory, blobs, phase-state) are this ticket's own placement onto the
// same closed twelve-domain set, documented per entry below. Every entry
// not listed here (audit, sessions, retrieval, queue, jobs, policy,
// ci_results, and — structurally — secrets/the vault) is local-only by
// omission (R-16.56): there is no fallthrough case that promotes them.
var coreDomainTable = map[registryKey]DomainClass{
	{storage.DomainConfig, "config"}: {
		Domain: storage.DomainConfig, Subkind: "config", Class: ClassSynced, Sensitivity: egress.TierInternal,
	},
	// R-21.223: "registry/account metadata onto the `config` domain".
	{storage.DomainConfig, "registry"}: {
		Domain: storage.DomainConfig, Subkind: "registry", Class: ClassSynced, Sensitivity: egress.TierInternal,
	},
	// Accounts sync as metadata only (Epic Q preamble §D-11: static keys
	// never ship) — server-primary, restricted tier by default so a
	// record must be explicitly re-tiered internal/public to leave the
	// server leg at all; filter.go's pre-serialization gate still applies
	// per record.
	{storage.DomainConfig, "accounts"}: {
		Domain: storage.DomainConfig, Subkind: "accounts", Class: ClassServerPrimary, Sensitivity: egress.TierRestricted,
	},
	// This ticket's own placement: PBD phase/board state is control-plane
	// configuration, not conversational content, so it maps onto `config`
	// rather than `context` — no PEWS-state DomainID exists to map onto
	// instead (git log shows no storage.DomainX assignment for phase
	// state as of this ticket).
	{storage.DomainConfig, "phase-state"}: {
		Domain: storage.DomainConfig, Subkind: "phase-state", Class: ClassSynced, Sensitivity: egress.TierInternal,
	},
	{storage.DomainMemory, "memory"}: {
		Domain: storage.DomainMemory, Subkind: "memory", Class: ClassSynced, Sensitivity: egress.TierInternal,
	},
	// R-21.223: "Conversation records map onto the `context` domain".
	{storage.DomainContext, "conversation"}: {
		Domain: storage.DomainContext, Subkind: "conversation", Class: ClassSynced, Sensitivity: egress.TierInternal,
	},
	{storage.DomainBlobs, "blobs"}: {
		Domain: storage.DomainBlobs, Subkind: "blobs", Class: ClassSynced, Sensitivity: egress.TierInternal,
	},
}

// Lookup returns domain/subkind's registered DomainClass. ok is false for
// any pair not in coreDomainTable, in which case the caller's contract is
// "local-only, never sync" (R-16.56) — callers must not synthesize a
// synced default on a miss.
func Lookup(domain storage.DomainID, subkind string) (DomainClass, bool) {
	dc, ok := coreDomainTable[registryKey{domain, subkind}]
	return dc, ok
}

// AllCoreClasses returns every registered core DomainClass, for
// diagnostics and tests. Order is unspecified.
func AllCoreClasses() []DomainClass {
	out := make([]DomainClass, 0, len(coreDomainTable))
	for _, dc := range coreDomainTable {
		out = append(out, dc)
	}
	return out
}

// PluginSyncDecl is a plugin manifest v2's `storage.sync` declaration
// (O/S-32.T3). An absent declaration (the zero value, Class == "") is
// local-only per R-16.56 — PluginClass never defaults to synced.
type PluginSyncDecl struct {
	Class Class
}

// PluginClass resolves decl to the DomainClass a plugin-scoped domain
// syncs under. A zero or invalid Class, or one outside
// {local-only, synced-append, server-primary} (synced is core-domain-only
// per 00-VISION's plugin key), resolves to local-only.
func PluginClass(pluginID string, decl PluginSyncDecl) DomainClass {
	switch decl.Class {
	case ClassSyncedAppend, ClassServerPrimary:
		return DomainClass{Domain: storage.DomainConfig, Subkind: "plugin." + pluginID, Class: decl.Class, Sensitivity: egress.TierRestricted}
	case ClassLocalOnly, ClassSynced, "":
		return DomainClass{Domain: storage.DomainConfig, Subkind: "plugin." + pluginID, Class: ClassLocalOnly, Sensitivity: egress.TierLocalOnly}
	default:
		// An unrecognized declared class string also fails closed to
		// local-only (R-16.56) — this default exists only for a future
		// Class value this switch has not yet been amended to name;
		// every KNOWN Class is listed above so exhaustive still catches
		// a forgotten case if the enum grows.
		return DomainClass{Domain: storage.DomainConfig, Subkind: "plugin." + pluginID, Class: ClassLocalOnly, Sensitivity: egress.TierLocalOnly}
	}
}
