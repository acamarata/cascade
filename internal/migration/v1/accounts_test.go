package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestAccountsImporter_GoldenCreatesMetadataAndReauth(t *testing.T) {
	root := stageFixture(t, "accounts/accounts.json", ".cascade/accounts/accounts.json")
	store, _ := testRegistry(t)
	result, err := NewAccountsImporter(store).Import(context.Background(), Request{SourceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeltaCount() != 1 || len(result.Reauth) != 1 || len(result.Journal) != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Reauth[0].Account != "openai.example-acc1" || result.Reauth[0].Methods[0] != "codex-cli" {
		t.Fatalf("missing re-auth evidence: %+v", result.Reauth)
	}
	record, err := store.GetProvider(context.Background(), "openai.example-acc1")
	if err != nil {
		t.Fatal(err)
	}
	if record.Driver != registry.DriverOpenAICompat || record.Auth != registry.AuthOAuth ||
		record.AuthRef != "v1.account.example-acc1" || record.HealthStatus != registry.HealthUnknown {
		t.Fatalf("metadata translation drifted: %+v", record)
	}
}

func TestAccountsImporter_ExistingWins(t *testing.T) {
	root := stageFixture(t, "accounts/accounts.json", ".cascade/accounts/accounts.json")
	store, _ := testRegistry(t)
	existing := validProvider("openai.example-acc1")
	existing.BaseURL = "https://existing.invalid"
	if err := store.UpsertProvider(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	result, err := NewAccountsImporter(store).Import(context.Background(), Request{SourceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if !result.EmptyDelta() || result.Changes[0].Operation != OperationSkip {
		t.Fatalf("existing-wins was not visible: %+v", result)
	}
	got, err := store.GetProvider(context.Background(), existing.Name)
	if err != nil || got.BaseURL != existing.BaseURL {
		t.Fatalf("existing provider changed: %+v err=%v", got, err)
	}
}

func TestAccountsImporter_DryRunDoesNotWrite(t *testing.T) {
	root := stageFixture(t, "accounts/accounts.json", ".cascade/accounts/accounts.json")
	store, _ := testRegistry(t)
	result, err := NewAccountsImporter(store).Import(context.Background(), Request{SourceRoot: root, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.DeltaCount() != 1 {
		t.Fatalf("unexpected dry-run result: %+v", result)
	}
	if providers, listErr := store.ListProviders(context.Background()); listErr != nil || len(providers) != 0 {
		t.Fatalf("dry run wrote provider: %+v err=%v", providers, listErr)
	}
}

func TestAccountsImporter_FailClosedSchemas(t *testing.T) {
	base := readAccountGolden(t)
	cases := []struct {
		name string
		data []byte
		kind cascade.Kind
	}{
		{"malformed", []byte(`{"schema_version":`), cascade.KindIntegrity},
		{"future", bytes.Replace(base, []byte(`"schema_version": 1`), []byte(`"schema_version": 2`), 1), cascade.KindUnsupported},
		{"unknown field", bytes.Replace(base, []byte(`"schema_version": 1`), []byte(`"extra": true, "schema_version": 1`), 1), cascade.KindIntegrity},
		{"bad time", bytes.Replace(base, []byte(`2026-09-12T19:28:30.278005+00:00`), []byte(`not-a-time`), 1), cascade.KindIntegrity},
		{"unknown family", bytes.Replace(base, []byte(`"family": "openai"`), []byte(`"family": "future"`), 1), cascade.KindInvalidInput},
		{"unknown role", bytes.Replace(base, []byte(`"role": "pooled"`), []byte(`"role": "owner"`), 1), cascade.KindInvalidInput},
		{"unknown access", bytes.Replace(base, []byte(`"codex-cli"`), []byte(`"new-cli"`), 1), cascade.KindInvalidInput},
		{"missing scalar", bytes.Replace(base, []byte(`"key_count": 0,`), nil, 1), cascade.KindIntegrity},
		{"unknown task", bytes.Replace(base, []byte(`"background"`), []byte(`"future-task"`), 1), cascade.KindInvalidInput},
		{"unknown route", bytes.Replace(base, []byte(`"account_id": "example-acc1"`), []byte(`"account_id": "absent"`), 1), cascade.KindInvalidInput},
		{"trailing", append(append([]byte(nil), base...), []byte(` {}`)...), cascade.KindIntegrity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := testRegistry(t)
			_, err := importAccountBytes(t, store, tc.data, false)
			assertKind(t, err, tc.kind)
			providers, listErr := store.ListProviders(context.Background())
			if listErr != nil || len(providers) != 0 {
				t.Fatalf("refusal left provider rows: %+v err=%v", providers, listErr)
			}
		})
	}
}

func TestAccountsImporter_RefusesUnmappableKnownV1Family(t *testing.T) {
	base := readAccountGolden(t)
	data := bytes.ReplaceAll(base, []byte(`"family": "openai"`), []byte(`"family": "opencode"`))
	store, _ := testRegistry(t)
	_, err := importAccountBytes(t, store, data, false)
	assertKind(t, err, cascade.KindInvalidInput)
	assertSentinel(t, err, ErrUnknownInput)
}

func TestAccountsImporter_SourceErrorsAreTyped(t *testing.T) {
	_, _, err := readAccountsSource("")
	assertKind(t, err, cascade.KindInvalidInput)
	_, _, err = readAccountsSource(t.TempDir())
	assertKind(t, err, cascade.KindNotFound)
	_, err = NewAccountsImporter(nil).Import(context.Background(), Request{SourceRoot: t.TempDir()})
	assertKind(t, err, cascade.KindInvalidInput)

	root := t.TempDir()
	writeSource(t, root, ".cascade/accounts.json", readAccountGolden(t))
	writeSource(t, root, ".cascade/accounts/accounts.json", readAccountGolden(t))
	_, _, err = readAccountsSource(root)
	assertKind(t, err, cascade.KindConflict)
}

func TestAccountsDecoder_RejectsDuplicateAndEmptyAccess(t *testing.T) {
	base := readAccountGolden(t)
	duplicate := bytes.Replace(base, []byte(`"accounts": [`), []byte(`"accounts": [`), 1)
	var registryValue v1AccountsRegistry
	if err := jsonUnmarshalForTest(duplicate, &registryValue); err != nil {
		t.Fatal(err)
	}
	registryValue.Accounts = append(registryValue.Accounts, registryValue.Accounts[0])
	data := jsonMarshalForTest(t, registryValue)
	_, err := decodeV1Accounts(data)
	assertKind(t, err, cascade.KindInvalidInput)

	registryValue.Accounts = registryValue.Accounts[:1]
	registryValue.Accounts[0].AccessMethods = nil
	_, err = decodeV1Accounts(jsonMarshalForTest(t, registryValue))
	assertKind(t, err, cascade.KindIntegrity)
}

func readAccountGolden(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "v1-goldens", "accounts", "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func importAccountBytes(t *testing.T, store *registry.Registry, data []byte, dry bool) (DryRunResult, error) {
	t.Helper()
	root := t.TempDir()
	writeSource(t, root, ".cascade/accounts/accounts.json", data)
	return NewAccountsImporter(store).Import(context.Background(), Request{SourceRoot: root, DryRun: dry})
}

func validProvider(name string) registry.ProviderRecord {
	return registry.ProviderRecord{Name: name, Driver: registry.DriverAnthropic,
		Auth: registry.AuthOAuth, AuthRef: "existing.auth", AccountKind: registry.AccountPersonal,
		Tier: registry.TierStrong, HealthStatus: registry.HealthHealthy}
}

func jsonUnmarshalForTest(data []byte, out interface{}) error {
	return json.Unmarshal(data, out)
}

func jsonMarshalForTest(t *testing.T, value interface{}) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
