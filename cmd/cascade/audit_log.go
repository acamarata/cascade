// Purpose: the one place cmd/cascade builds an audit log. Every production
//
//	writer (the conductor, policy and its reloads, hooks, supervision, the
//	backup and plugin verbs, the GitHub CI verbs) takes its *audit.Log
//	from openAuditLog, so every record passes the same secrets substitution
//	pass (exact vault values, then the process detector) over its `action`
//	and `explain` fields before it is sealed, and every writer in the
//	process shares one Log per store. The pass does not reach `outcome` or
//	`policy_snapshot` (internal/audit/redact.go owns the field list).
//
// Inputs: the store, an injected clock, an optional bus and the data-dir
//
//	provider that binds the process firewall (egress_firewall.go).
//
// Outputs: the shared *audit.Log, the *audit.EffectLog over it, or a
//
//	read-only verifier; an error when the vault cannot be reached, in which
//	case the caller refuses its audit-dependent subsystem or verb.
//
// Constraints: fail closed. No custody means no log: there is no fallback
//
//	that builds an unredacted Log. internal/build's arch gate
//	(TestArchAuditNewOnlyInHelper) keeps audit.New and
//	audit.NewWithRedactor out of every other shipping file. Failures are
//	never memoized. A nil store (a daemon test composition with no store)
//	or one whose dynamic type cannot be a map key (the lookup recovers from
//	the hash panic) is not shared: it gets a fresh Log per call. A Log over
//	a nil store refuses every append and every Verify with KindUnavailable;
//	the head compare-and-swap keeps unshared Logs chain-safe.
//
// SPORT: cmd/cascade/audit_log (ADD, P1-SEC-06).
package main

import (
	"context"
	"sync"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// jsonEscapeDepth is how many levels of JSON string escaping the redactor
// looks for: a field, JSON text inside its string, and JSON text inside that.
const jsonEscapeDepth = 3

// auditRedactor is the substitution pass as audit.Redactor: the exact-value
// half over the process vault, then the process detector's half.
type auditRedactor struct{ vault egress.Vault }

// Redact returns content with vault values and detected credentials
// replaced by typed tags, or an error and no content. The vault is read
// once. Every occurrence of every vault value is found as stored and as
// encoding/json escapes it (up to three levels, with and without HTML
// escaping), because a value holding a quote, a backslash, an angle bracket
// or an ampersand never appears literally in a record's JSON explain, and
// appears escaped again when JSON text is carried in a string or quoted in
// an action. All hits go to one rewrite, so the rewriter's largest-span-first
// rule decides every overlap across values and forms. The standard exact and
// detector pass then runs once over what is left. Escapes encoding/json does
// not write (`\/`, `\u00e4`) are not matched.
func (r auditRedactor) Redact(ctx context.Context, content []byte) ([]byte, error) {
	detector, err := secrets.ProcessDetector()
	if err != nil {
		return nil, err
	}
	snap, err := secrets.NewSnapshotVault(ctx, r.vault)
	if err != nil {
		return nil, err
	}
	rewriter := secrets.NewRewriter()
	rewritten, err := rewriter.Rewrite(content, snap.ExactHits(content, jsonEscapeDepth))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err, "audit: the exact-value rewrite could not be applied; nothing is written")
	}
	return egress.SubstitutionPass(ctx, snap, detector, rewriter, rewritten.Text)
}

// auditLogMemo holds the process's audit logs and the effect log verified
// over each, under one lock.
var auditLogMemo struct {
	mu      sync.Mutex
	logs    map[provider.Store]*audit.Log
	effects map[*audit.Log]*audit.EffectLog
}

// openAuditLog returns the process's redacting audit log over store: the
// same *audit.Log for the same store every time. The process firewall is
// bound (or checked against paths) on every call, a memo hit included, so a
// caller naming another data directory gets the same refusal whether or not
// the store was opened before. The first successful call builds the log (its
// clock and bus are the ones it keeps); an error is KindUnavailable or
// KindIntegrity from the vault custody and is not remembered.
func openAuditLog(store provider.Store, clock runtime.Clock, bus audit.Publisher, paths runtime.PathProvider) (*audit.Log, error) {
	fw, err := processFirewallFor(paths)
	if err != nil {
		return nil, err
	}
	auditLogMemo.mu.Lock()
	defer auditLogMemo.mu.Unlock()
	if log := sharedAuditLog(store); log != nil {
		return log, nil
	}
	log := audit.NewWithRedactor(store, clock, bus, auditRedactor{vault: fw.vault})
	shareAuditLog(store, log)
	return log, nil
}

// sharedAuditLog returns the memoized log for store, or nil. A nil store,
// or one whose dynamic type cannot be a map key, is never shared. The caller
// holds auditLogMemo.mu.
func sharedAuditLog(store provider.Store) (log *audit.Log) {
	defer func() {
		if recover() != nil {
			log = nil
		}
	}()
	if store == nil {
		return nil
	}
	return auditLogMemo.logs[store]
}

// shareAuditLog memoizes log for store unless store cannot be a map key.
// The caller holds auditLogMemo.mu.
func shareAuditLog(store provider.Store, log *audit.Log) {
	defer func() { _ = recover() }()
	if store == nil {
		return
	}
	if auditLogMemo.logs == nil {
		auditLogMemo.logs = map[provider.Store]*audit.Log{}
	}
	auditLogMemo.logs[store] = log
}

// openDaemonAuditLog is openAuditLog for the daemon's composition root: it
// first binds the process firewall to the daemon's own data directory (the
// owner binding conductorSecurity and the egress-firewall wiring step use),
// so which wiring step reaches the log first never decides whether the
// vault is the daemon's.
func openDaemonAuditLog(store provider.Store, clock runtime.Clock, bus audit.Publisher, paths runtime.PathProvider) (*audit.Log, error) {
	if _, err := bindDaemonFirewall(paths); err != nil {
		return nil, err
	}
	return openAuditLog(store, clock, bus, paths)
}

// openEffectLog returns the effect log over the process's audit log for
// store. The first call per Log verifies the whole log, including the
// effect index against the chained effect records, and refuses when that
// fails: effects recorded over a damaged log could be begun twice. A
// tampered log is KindIntegrity; any other Verify failure (the store
// unavailable, the log moving too fast to check) keeps its own Kind. The
// check runs outside the memo lock, so it never stalls another store's open.
func openEffectLog(store provider.Store, clock runtime.Clock, bus audit.Publisher, paths runtime.PathProvider) (*audit.EffectLog, error) {
	log, err := openAuditLog(store, clock, bus, paths)
	if err != nil {
		return nil, err
	}
	auditLogMemo.mu.Lock()
	el := auditLogMemo.effects[log]
	auditLogMemo.mu.Unlock()
	if el != nil {
		return el, nil
	}
	if err := log.Verify(context.Background()); err != nil {
		return nil, err
	}
	if el, err = audit.NewEffectLog(log); err != nil {
		return nil, err
	}
	auditLogMemo.mu.Lock()
	defer auditLogMemo.mu.Unlock()
	if sharedAuditLog(store) != log {
		return el, nil // an unshared log (nil or unhashable store) is not remembered
	}
	if auditLogMemo.effects == nil {
		auditLogMemo.effects = map[*audit.Log]*audit.EffectLog{}
	}
	if known := auditLogMemo.effects[log]; known != nil {
		return known, nil
	}
	auditLogMemo.effects[log] = el
	return el, nil
}

// auditVerifier is the read-only half of an audit log.
type auditVerifier interface {
	Verify(ctx context.Context) error
}

// chainVerifier exposes Verify and nothing else of the Log it wraps, so a
// type assertion on the verifier cannot reach an appender.
type chainVerifier struct{ log *audit.Log }

// Verify checks the whole log: records, chain, tail and effect index.
func (v chainVerifier) Verify(ctx context.Context) error { return v.log.Verify(ctx) }

// openAuditVerifier returns a read-only chain verifier over store, for
// doctor. It never appends and needs no custody: Verify reads only.
func openAuditVerifier(store provider.Store) auditVerifier {
	return chainVerifier{log: audit.New(store, runtime.NewSystemClock(), nil)}
}
