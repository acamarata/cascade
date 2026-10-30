// Package gateway defines guarded gateway accounts and call-time credentials.
package gateway

import (
	"encoding/json"
	"sort"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// CredentialKind identifies the credential source; zero is invalid.
type CredentialKind string

const (
	// CredentialEnv reads an environment variable.
	CredentialEnv CredentialKind = "env"
	// CredentialHelper executes a pinned helper.
	CredentialHelper CredentialKind = "helper"
)

// HeaderStyle identifies the authentication header.
type HeaderStyle string

const (
	// HeaderBearer selects Authorization: Bearer.
	HeaderBearer HeaderStyle = "bearer"
	// HeaderXAPIKey selects x-api-key.
	HeaderXAPIKey HeaderStyle = "x-api-key"
)

// CatalogSource identifies how the catalog is obtained.
type CatalogSource string

const (
	// CatalogDeclared uses configured models.
	CatalogDeclared CatalogSource = "declared"
	// CatalogDiscovered discovers models at /v1/models.
	CatalogDiscovered CatalogSource = "discovered"
)

// Credential names exactly one call-time token source.
type Credential struct {
	Kind                             CredentialKind
	EnvVar, HelperPath, HelperSHA256 string
}

// UsageSource describes an optional read-only quota source.
type UsageSource struct {
	Kind, Window, Path                                             string
	UsedPointer, LimitPointer, UtilizationPointer, ResetsAtPointer string
	UtilizationHeader, ResetsHeader                                string
}

// Account is a terms-sensitive gateway lane with a normalized URL identity.
type Account struct {
	Name, ID, BaseURL string
	Header            HeaderStyle
	Credential        Credential
	Models            []string
	Usage             UsageSource
	TermsSensitive    bool
}

// Catalog returns the configured catalog source.
func (a Account) Catalog() CatalogSource {
	if a.Models != nil {
		return CatalogDeclared
	}
	return CatalogDiscovered
}

// ParseAccounts validates a gateway table and returns accounts sorted by name.
func ParseAccounts(section map[string]any) ([]Account, error) {
	names := make([]string, 0, len(section))
	for name := range section {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []Account
	for _, name := range names {
		if !accountNameRE.MatchString(name) {
			return nil, invalid(name, "invalid account name")
		}
		m, ok := section[name].(map[string]any)
		if !ok {
			return nil, invalid(name, "must be a table")
		}
		a, err := parseOneAccount(name, m)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// AccountsFrom reads only the admitted gateway family.
func AccountsFrom(eff runtime.EffectiveConfig) ([]Account, error) {
	raw := eff.Families["gateways"]
	if len(raw) == 0 {
		return nil, nil
	}
	var accounts []Account
	if err := json.Unmarshal(raw, &accounts); err != nil {
		return nil, cascade.New(cascade.KindIntegrity, "gateways: invalid admitted account list")
	}
	return accounts, nil
}

// Lookup returns an account by name, or KindNotFound.
func Lookup(accts []Account, name string) (Account, error) {
	for _, a := range accts {
		if a.Name == name {
			return a, nil
		}
	}
	return Account{}, cascade.Newf(cascade.KindNotFound, "gateway account %q is not configured", name)
}
