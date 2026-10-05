// Purpose: the per-domain allowlist Redactor that decides which bytes of an
// importer's output may reach a committed, public fixture.
// Inputs: importer output values (memory frontmatter, registry records,
// DryRunResult entries, a parsed v2 config key map) and the input-relative
// path of the input that produced them.
// Outputs: plain maps ready for canonical JSON, in which every field is
// either allowlisted, mapped to a synthetic NAME-<n> / ACCOUNT-<n> /
// input-relative form, or replaced by REDACTED / REDACTED-PATH.
// Constraints: allowlist only, no key-name heuristics. A field this file does
// not name is kept with the value REDACTED, never omitted, and struct fields
// are enumerated by reflection so a field added to an importer type later
// is redacted by default. TestRedactAllowlist_KeysExistOnTypes proves every
// allowlisted name exists on its Go type.
// SPORT: migration/golden/ADD (P1-E26-W10-S54-T3).
package main

import (
	"reflect"
	"strconv"
	"strings"

	v1 "github.com/acamarata/cascade/internal/migration/v1"
)

// The redaction forms a fixture may carry in place of a value.
const (
	redacted     = "REDACTED"
	redactedPath = "REDACTED-PATH"
)

// memoryFrontmatterKeep are the v2 memory frontmatter keys the importer and
// the FileStore batch set from mapped input: name, kind and description from
// the v1 record, the importer's fixed scope, confidence and origin, the
// source reference, the source mtime and the body hash, plus the v2 format.
var memoryFrontmatterKeep = map[string]bool{
	"format": true, "name": true, "kind": true, "description": true,
	"scope_ref": true, "confidence": true, "origin": true, "session_id": true,
	"created_at": true, "updated_at": true, "content_hash": true,
}

// providerKeep are the registry.ProviderRecord fields kept verbatim.
// Name and AuthRef are mapped (providerRules); every other field is
// REDACTED.
var providerKeep = []string{"Driver", "Auth", "AccountKind", "Tier", "KnownModels"}

// dryRunKeep names, per domain, the v1.DryRunResult fields a record carries.
var dryRunKeep = map[v1.Domain][]string{
	v1.DomainMemory:   {"Changes"},
	v1.DomainVault:    {"Changes"},
	v1.DomainAccounts: {"Changes", "Journal", "Reauth"},
	v1.DomainConfig:   {"Changes"},
}

// changeKeep is the v1.Change field copied verbatim; changeRules maps the
// rest.
var changeKeep = []string{"Operation"}

// redactor carries the per-record synthetic name maps. One redactor serves
// exactly one harvested record, so NAME-<n> and ACCOUNT-<n> numbering is a
// pure function of that record's importer output order.
type redactor struct {
	domain   v1.Domain
	inputRel string
	names    map[string]string
	accounts map[string]string
}

// newRedactor returns a redactor for the record harvested from inputRel.
func newRedactor(domain v1.Domain, inputRel string) *redactor {
	return &redactor{domain: domain, inputRel: inputRel, names: map[string]string{}, accounts: map[string]string{}}
}

// name returns the NAME-<n> form of v, assigning the next n on first use.
func (r *redactor) name(v string) string {
	return assign(r.names, "NAME-", v)
}

// account returns the ACCOUNT-<n> form of v, assigning on first use.
func (r *redactor) account(v string) string {
	return assign(r.accounts, "ACCOUNT-", v)
}

// assign returns m[v], first storing prefix+<len(m)+1> when v is new.
func assign(m map[string]string, prefix, v string) string {
	if got, ok := m[v]; ok {
		return got
	}
	m[v] = prefix + strconv.Itoa(len(m)+1)
	return m[v]
}

// mapped returns the synthetic form already assigned to v by either map, or
// REDACTED. It never assigns: a value that only appears in free text is
// never given a name of its own.
func (r *redactor) mapped(v string) string {
	if got, ok := r.accounts[v]; ok {
		return got
	}
	if got, ok := r.names[v]; ok {
		return got
	}
	return redacted
}

// memoryFrontmatter keeps the allowlisted keys and REDACTs every other value.
func (r *redactor) memoryFrontmatter(fm map[string]string) map[string]any {
	out := make(map[string]any, len(fm))
	for key, value := range fm {
		out[key] = redacted
		if memoryFrontmatterKeep[key] {
			out[key] = value
		}
	}
	return out
}

// fieldRules maps a Go field name to the function producing its redacted
// value.
type fieldRules map[string]func(reflect.Value) any

// provider redacts one ProviderRecord, enumerating its fields by reflection.
func (r *redactor) provider(rec any) map[string]any {
	return redactStruct(rec, r.providerRules(), providerKeep)
}

// providerRules maps Name to ACCOUNT-<n> and AuthRef to NAME-<n>.
func (r *redactor) providerRules() fieldRules {
	return fieldRules{
		"Name":    func(v reflect.Value) any { return r.account(v.String()) },
		"AuthRef": func(v reflect.Value) any { return r.name(v.String()) },
	}
}

// change redacts one importer Change under this record's domain rules.
func (r *redactor) change(c v1.Change) map[string]any {
	return redactStruct(c, r.changeRules(), changeKeep)
}

// changeRules maps Source, Target and ContentHash per domain.
func (r *redactor) changeRules() fieldRules {
	return fieldRules{
		"Source":      func(v reflect.Value) any { return r.source(v.String()) },
		"Target":      func(v reflect.Value) any { return r.target(v.String()) },
		"ContentHash": func(v reflect.Value) any { return r.contentHash(v.String()) },
	}
}

// journal redacts one JournalEntry.
func (r *redactor) journal(e v1.JournalEntry) map[string]any {
	return redactStruct(e, r.journalRules(), nil)
}

// journalRules keeps Code by allowlist, maps Source to the input-relative
// path and passes Detail only through the synthetic maps.
func (r *redactor) journalRules() fieldRules {
	return fieldRules{
		"Code": func(v reflect.Value) any {
			if journalCodeKeep[v.String()] {
				return v.String()
			}
			return redacted
		},
		"Source": func(v reflect.Value) any { return r.source(v.String()) },
		"Detail": func(v reflect.Value) any { return r.mapped(v.String()) },
	}
}

// reauth redacts one ReauthPrompt.
func (r *redactor) reauth(p v1.ReauthPrompt) map[string]any {
	return redactStruct(p, r.reauthRules(), nil)
}

// reauthRules passes every ReauthPrompt field only through the maps.
func (r *redactor) reauthRules() fieldRules {
	return fieldRules{
		"Account": func(v reflect.Value) any { return r.mapped(v.String()) },
		"Driver":  func(v reflect.Value) any { return r.mapped(v.String()) },
		"Methods": func(v reflect.Value) any {
			out := make([]string, v.Len())
			for i := range out {
				out[i] = r.mapped(v.Index(i).String())
			}
			return out
		},
	}
}

// redactStruct renders a struct as a map keyed by each field's JSON name (or
// Go name when untagged). A field with a rule takes the rule's value, a field
// in keep is copied, and every other field becomes REDACTED.
func redactStruct(v any, rules fieldRules, keep []string) map[string]any {
	value := reflect.ValueOf(v)
	typ := value.Type()
	out := make(map[string]any, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		key := jsonName(field)
		switch rule, ok := rules[field.Name]; {
		case ok:
			out[key] = rule(value.Field(i))
		case contains(keep, field.Name):
			out[key] = value.Field(i).Interface()
		default:
			out[key] = redacted
		}
	}
	return out
}

// jsonName returns a field's JSON name, or its Go name when untagged.
func jsonName(field reflect.StructField) string {
	name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
	if name == "" || name == "-" {
		return field.Name
	}
	return name
}

// contains reports whether list holds s.
func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// dryRun renders the DryRunResult fields dryRunKeep selects for this
// record's domain, each entry through its own rule table.
func (r *redactor) dryRun(res v1.DryRunResult) map[string]any {
	out := map[string]any{}
	for _, field := range dryRunKeep[r.domain] {
		switch field {
		case "Changes":
			out["changes"] = mapEach(res.Changes, r.change)
		case "Journal":
			out["journal"] = mapEach(res.Journal, r.journal)
		case "Reauth":
			out["reauth"] = mapEach(res.Reauth, r.reauth)
		}
	}
	return out
}

// mapEach applies fn to every item, returning a non-nil slice.
func mapEach[T any](items []T, fn func(T) map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, fn(item))
	}
	return out
}
