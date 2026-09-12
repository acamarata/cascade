// Package v1 uses this file to translate account metadata to the provider registry.
// Purpose: translate v1 account metadata to the established provider registry.
// Inputs: schema-version 1 .cascade/accounts/accounts.json.
// Outputs: transactionally created providers, existing-wins skips, re-auth
// prompts, and journal evidence for known metadata without a v2 store field.
// Constraints: credentials are never copied; unknown fields or enums refuse;
// the registry transaction prevents a half-written account batch.
// SPORT: migration/v1/accounts/ADD (P1-E26-W10-S53-T1).
package v1

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/zeebo/blake3"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/cascade"
)

const maxAccountsBytes = 32 << 20

type accountsImporter struct{ store *registry.Registry }

// NewAccountsImporter returns the accounts importer through the sole Importer
// interface. The registry handle is retained privately.
func NewAccountsImporter(store *registry.Registry) Importer {
	return &accountsImporter{store: store}
}

func (a *accountsImporter) Import(ctx context.Context, req Request) (DryRunResult, error) {
	result := DryRunResult{Domain: DomainAccounts}
	if a.store == nil {
		return result.Normalize(), cascade.New(cascade.KindInvalidInput, "migration v1 accounts: registry is required")
	}
	data, source, err := readAccountsSource(req.SourceRoot)
	if err != nil {
		return result.Normalize(), err
	}
	legacy, err := decodeV1Accounts(data)
	if err != nil {
		return result.Normalize(), err
	}
	records, prompts, journal := translateV1Accounts(legacy, source)
	report, err := a.store.ImportProviders(ctx, records, req.DryRun)
	if err != nil {
		return result.Normalize(), err
	}
	result.Applied, result.Reauth, result.Journal = !req.DryRun, prompts, journal
	appendAccountChanges(&result, source, report)
	return result.Normalize(), nil
}

func readAccountsSource(root string) ([]byte, string, error) {
	if strings.TrimSpace(root) == "" {
		return nil, "", cascade.New(cascade.KindInvalidInput, "migration v1 accounts: source root is required")
	}
	candidates := []string{
		filepath.Join(root, ".cascade", "accounts", "accounts.json"),
		filepath.Join(root, ".cascade", "accounts.json"),
	}
	path, err := oneRegularSource(candidates, "accounts")
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", cascade.Wrap(cascade.KindUnavailable, err, "migration v1 accounts: read source")
	}
	if len(data) > maxAccountsBytes {
		return nil, "", cascade.Wrap(cascade.KindInvalidInput, ErrUnknownInput,
			"migration v1 accounts: source exceeds the size limit")
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, "", cascade.Wrap(cascade.KindInternal, err, "migration v1 accounts: derive source name")
	}
	return data, filepath.ToSlash(rel), nil
}

func oneRegularSource(candidates []string, domain string) (string, error) {
	found := ""
	for _, path := range candidates {
		info, err := os.Lstat(path)
		switch {
		case err == nil && info.Mode().IsRegular():
			if found != "" {
				return "", cascade.Wrapf(cascade.KindConflict, ErrImportConflict,
					"migration v1 %s: multiple source files exist", domain)
			}
			found = path
		case err == nil:
			return "", cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownInput,
				"migration v1 %s: source is not a regular file", domain)
		case !os.IsNotExist(err):
			return "", cascade.Wrapf(cascade.KindUnavailable, err,
				"migration v1 %s: inspect source", domain)
		}
	}
	if found == "" {
		return "", cascade.Newf(cascade.KindNotFound, "migration v1 %s: source file not found", domain)
	}
	return found, nil
}

func translateV1Accounts(source v1AccountsRegistry, sourcePath string) ([]registry.ProviderRecord, []ReauthPrompt, []JournalEntry) {
	records := make([]registry.ProviderRecord, 0, len(source.Accounts))
	prompts := make([]ReauthPrompt, 0, len(source.Accounts))
	journal := make([]JournalEntry, 0, len(source.Accounts)+1)
	for _, account := range source.Accounts {
		driver, _ := mapV1Driver(account.Family)
		kind, tier, _ := mapV1Role(account.Role)
		auth := registry.AuthOAuth
		if *account.KeyCount > 0 {
			auth = registry.AuthKey
		}
		name := account.Family + "." + account.ID
		records = append(records, registry.ProviderRecord{
			Name: name, Driver: driver, Auth: auth,
			AuthRef:     registry.VaultKeyRef("v1.account." + account.ID),
			KnownModels: account.Models, AccountKind: kind, Tier: tier,
			HealthStatus: registry.HealthUnknown,
		})
		prompts = append(prompts, ReauthPrompt{Account: name, Driver: string(driver), Methods: account.AccessMethods})
		journal = append(journal, accountMetadataJournal(account, sourcePath))
	}
	journal = append(journal, matrixJournal(source.ModelMatrix, sourcePath))
	return records, prompts, journal
}

func accountMetadataJournal(account v1Account, source string) JournalEntry {
	metadata, _ := json.Marshal(account)
	detail := "subscription, role, priority, CLI availability, key count, quota link, and notes accounted for; metadata hash=" + digestBytes(metadata)
	return JournalEntry{Code: "accounts.metadata", Source: source + "#" + account.ID, Detail: detail}
}

func matrixJournal(matrix []v1ModelMatrix, source string) JournalEntry {
	data, _ := json.Marshal(matrix)
	return JournalEntry{
		Code: "accounts.model-matrix", Source: source,
		Detail: "routing matrix validated and retained by digest; entries=" + intString(len(matrix)) + " hash=" + digestBytes(data),
	}
}

func appendAccountChanges(result *DryRunResult, source string, report registry.ProviderImportResult) {
	for _, name := range report.Created {
		result.Changes = append(result.Changes, Change{Operation: OperationCreate, Source: source, Target: name})
	}
	for _, name := range report.Existing {
		result.Changes = append(result.Changes, Change{Operation: OperationSkip, Source: source, Target: name})
	}
}

func digestBytes(data []byte) string {
	sum := blake3.Sum256(data)
	return hex.EncodeToString(sum[:])
}
