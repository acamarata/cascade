// Package policy (denylist_store.go): Purpose: StoreDenyList, the
//
//	B-layer-backed DenyListEngine. It holds the operator's rows in the
//	`policy` storage domain beside S-17.T1's grant rows, caches them in
//	memory, and invalidates that cache on every write.
//
// Inputs: a provider.Store (the `policy` domain namespace), and the
//
//	pattern, class and command text a caller asks about.
//
// Outputs: persisted DenyRule rows, the match answer, or a typed refusal.
// Constraints: writes and reads go through the B-layer abstraction only,
//
//	never direct SQLite. EVERY refusal direction denies: an unreadable
//	store, an undecodable row, a row whose stored pattern no longer
//	satisfies the grammar, and a command the normalizer cannot resolve all
//	return an error, and layer 1 reads a deny-lister error as a match. A
//	row is never silently skipped: a listing that quietly omitted a
//	corrupted deny-list row would hide the entry an operator is relying on.
//
//	The cache is a read-through of rows that already passed Validate, so
//	an invalidated cache costs a re-read and never a re-interpretation.
//
// SPORT: internal/policy StoreDenyList/ADDED (P1-E09-W2-S17-T4).
package policy

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// denyKeyPrefix namespaces deny-list rows inside the `policy` domain,
// leaving room for the grant rows S-17.T1 writes to the same namespace.
const denyKeyPrefix = "denylist/"

// StoreDenyList is the real DenyListEngine (Art.1) over provider.Store.
type StoreDenyList struct {
	store provider.Store
	// mu guards the cache. It is an RWMutex because Denied is on the
	// evaluation hot path and Add/Remove are operator actions.
	mu     sync.RWMutex
	rules  []DenyRule
	loaded bool
}

var _ DenyListEngine = (*StoreDenyList)(nil)

// NewStoreDenyList builds the engine over store. The store is required: an
// engine with nowhere to read from could only answer by assuming the list
// is empty, and that assumption is a widening.
func NewStoreDenyList(store provider.Store) (*StoreDenyList, error) {
	if store == nil {
		return nil, cascade.New(cascade.KindInvalidInput,
			"policy: deny-list engine requires a store")
	}
	return &StoreDenyList{store: store}, nil
}

// namespace is the `policy` storage domain, read from storage's own
// constant so the two can never drift.
func (s *StoreDenyList) namespace() string { return string(storage.DomainPolicy) }

// denyKey addresses a row by its pattern. The pattern is hex-encoded so
// every byte an operator may legitimately write in a glob round-trips
// through a key namespace without needing an escaping rule of its own.
func denyKey(pattern string) string {
	return denyKeyPrefix + hex.EncodeToString([]byte(pattern))
}

// Add implements DenyListEngine. The row is validated BEFORE it is
// written, so a pattern the matcher would later refuse never reaches
// storage.
func (s *StoreDenyList) Add(ctx context.Context, pattern string, class ActionClass) error {
	rule := DenyRule{Pattern: pattern, Class: class}
	if err := rule.Validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(rule)
	if err != nil {
		return newStoreError(err, "the deny-list row could not be encoded")
	}
	if err := s.store.Put(ctx, s.namespace(), denyKey(pattern), encoded); err != nil {
		return newStoreError(err, "the deny-list row could not be written")
	}
	s.invalidate()
	return nil
}

// Remove implements DenyListEngine. Deleting an absent pattern is not an
// error: the caller's intent (this pattern must not be listed) holds
// either way.
func (s *StoreDenyList) Remove(ctx context.Context, pattern string) error {
	if err := s.store.Delete(ctx, s.namespace(), denyKey(pattern)); err != nil {
		return newStoreError(err, "the deny-list row could not be removed")
	}
	s.invalidate()
	return nil
}

// List implements DenyListEngine, in pattern order so two calls against
// unchanged storage return the same slice and no decision depends on map
// or scan iteration order.
func (s *StoreDenyList) List(ctx context.Context) ([]DenyRule, error) {
	rules, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]DenyRule, len(rules))
	copy(out, rules)
	return out, nil
}

// Denied implements DenyLister: the match Match names in the contract. The
// input is the R-21.212 recursively normalized form set, so a deny-listed
// operation reached through a wrapper, a nested shell, a substitution, a
// chained operator or shell quoting matches the same row the bare form
// matches, and a form the normalizer refuses is an error rather than a
// pass.
func (s *StoreDenyList) Denied(ctx context.Context, action string) (bool, error) {
	rules, err := s.load(ctx)
	if err != nil {
		return false, err
	}
	forms, err := normalizedForms(ctx, action)
	if err != nil {
		return false, err
	}
	return matchesAny(rules, forms, anyActionClass, false)
}

// ContainsClass implements DenyListEngine (R-21.4, R-21.233): the same
// rows, queried by action class instead of by raw command text. A row
// scoped to class answers for class; a wildcard row answers for every
// class; a row scoped to another class does not answer at all.
func (s *StoreDenyList) ContainsClass(
	ctx context.Context, class ActionClass, action string,
) (bool, error) {
	rules, err := s.load(ctx)
	if err != nil {
		return false, err
	}
	forms, err := normalizedForms(ctx, action)
	if err != nil {
		return false, err
	}
	return matchesAny(rules, forms, class, true)
}

// matchesAny is the one matching routine both query shapes run. When
// scoped is set, only rows answering for class are considered; otherwise
// every row is. A stored pattern that no longer satisfies the grammar is
// an ERROR, never a skip.
func matchesAny(
	rules []DenyRule, forms []denyForm, class ActionClass, scoped bool,
) (bool, error) {
	for _, rule := range rules {
		if err := validateDenyPattern(rule.Pattern); err != nil {
			return false, err
		}
		if scoped && !rule.scopedTo(class) {
			continue
		}
		for _, form := range forms {
			if globMatch(rule.Pattern, form.text, form.partial) {
				return true, nil
			}
		}
	}
	return false, nil
}

// invalidate drops the cache, so the next read re-reads storage. Add and
// Remove call it after the write lands, never before.
func (s *StoreDenyList) invalidate() {
	s.mu.Lock()
	s.rules, s.loaded = nil, false
	s.mu.Unlock()
}

// load returns the cached rows, reading storage on the first call after a
// write. A read that fails leaves the cache empty and returns the refusal:
// there is no path here that caches a partial list.
func (s *StoreDenyList) load(ctx context.Context) ([]DenyRule, error) {
	s.mu.RLock()
	if s.loaded {
		rules := s.rules
		s.mu.RUnlock()
		return rules, nil
	}
	s.mu.RUnlock()

	rules, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.rules, s.loaded = rules, true
	s.mu.Unlock()
	return rules, nil
}

// read scans every deny-list row out of the `policy` domain and validates
// each one. It sorts the result, so the order a decision walks does not
// depend on the store's scan order.
func (s *StoreDenyList) read(ctx context.Context) ([]DenyRule, error) {
	it, err := s.store.Scan(ctx, s.namespace(), denyKeyPrefix)
	if err != nil {
		return nil, newStoreError(err, "the deny-list could not be scanned")
	}
	defer func() { _ = it.Close() }()
	rules, err := collectDenyRules(ctx, it)
	if err != nil {
		return nil, err
	}
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].Pattern != rules[j].Pattern {
			return rules[i].Pattern < rules[j].Pattern
		}
		return rules[i].Class < rules[j].Class
	})
	return rules, nil
}

// collectDenyRules drains it, decoding and re-validating every row. A row
// that will not decode or will not validate fails the whole read.
func collectDenyRules(ctx context.Context, it provider.Iterator) ([]DenyRule, error) {
	var out []DenyRule
	for it.Next(ctx) {
		var rule DenyRule
		if err := json.Unmarshal(it.Value(), &rule); err != nil {
			return nil, newStoreError(nil, "the deny-list row at "+
				quoteName(sanitize(it.Key()))+" could not be decoded")
		}
		if err := rule.Validate(); err != nil {
			return nil, err
		}
		if want := denyKey(rule.Pattern); !strings.EqualFold(want, it.Key()) {
			return nil, newStoreError(nil, "the deny-list row at "+
				quoteName(sanitize(it.Key()))+" is stored under another pattern's key")
		}
		out = append(out, rule)
	}
	if err := it.Err(); err != nil {
		return nil, newStoreError(err, "the deny-list could not be read to the end")
	}
	return out, nil
}
