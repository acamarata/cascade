package main

// Purpose: the Redactor's allowlist contract: every allowlisted name exists
// on its Go type, the config allowlist equals what the real translator
// writes, and every unclassified field is kept with the value REDACTED.

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	v1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/providers/registry"
)

func TestRedactAllowlist_KeysExistOnTypes(t *testing.T) {
	r := newRedactor(v1.DomainAccounts, "accounts/accounts.json")
	checks := []struct {
		typ   reflect.Type
		names []string
	}{
		{reflect.TypeOf(registry.ProviderRecord{}), append(append([]string(nil), providerKeep...), ruleKeys(r.providerRules())...)},
		{reflect.TypeOf(v1.Change{}), append(append([]string(nil), changeKeep...), ruleKeys(r.changeRules())...)},
		{reflect.TypeOf(v1.JournalEntry{}), ruleKeys(r.journalRules())},
		{reflect.TypeOf(v1.ReauthPrompt{}), ruleKeys(r.reauthRules())},
	}
	for _, fields := range dryRunKeep {
		checks = append(checks, struct {
			typ   reflect.Type
			names []string
		}{reflect.TypeOf(v1.DryRunResult{}), fields})
	}
	for _, check := range checks {
		if len(check.names) == 0 {
			t.Fatalf("%s has an empty allowlist", check.typ.Name())
		}
		for _, name := range check.names {
			if _, ok := check.typ.FieldByName(name); !ok {
				t.Errorf("allowlisted field %s does not exist on %s", name, check.typ.Name())
			}
		}
	}
}

// ruleKeys returns a rule table's field names, sorted.
func ruleKeys(rules fieldRules) []string {
	keys := make([]string, 0, len(rules))
	for key := range rules {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestRedactAllowlist_ConfigKeysMatchTranslator(t *testing.T) {
	home := t.TempDir()
	source := "schema_version = 1\n[daemon]\nlog_level = \"info\"\nlog_format = \"json\"\nsocket_path = \"run/d.sock\"\n" +
		"[telemetry]\nenabled = false\n"
	if err := stage(home, ".cascade/config.toml", []byte(source)); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "config.toml")
	res, err := v1.NewConfigImporter(dest).Import(context.Background(), v1.Request{SourceRoot: home})
	if err != nil {
		t.Fatalf("translator refused the full known key set: %v", err)
	}
	flat, err := readV2ConfigKeys(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(flat) != len(configKeyKeep) {
		t.Errorf("translator wrote %d keys, allowlist holds %d", len(flat), len(configKeyKeep))
	}
	for key := range flat {
		if !configKeyKeep[key] {
			t.Errorf("translator writes %s, which the allowlist does not keep", key)
		}
	}
	for _, change := range res.Changes {
		_, src, _ := strings.Cut(change.Source, "#")
		if !configSourceKeep[src] || !configKeyKeep[change.Target] {
			t.Errorf("translator maps %s -> %s outside the allowlists", src, change.Target)
		}
	}
	if len(res.Changes) != len(configSourceKeep) {
		t.Errorf("translator mapped %d keys, source allowlist holds %d", len(res.Changes), len(configSourceKeep))
	}
}

func TestRedactorUnknownKeyRedacted(t *testing.T) {
	r := newRedactor(v1.DomainConfig, "config/x.toml")
	got := r.configKeys(map[string]any{"ORG_NOTE": "private", "logging.level": "info"})
	if got["ORG_NOTE"] != redacted {
		t.Errorf("config ORG_NOTE = %v, want REDACTED", got["ORG_NOTE"])
	}
	if got["logging.level"] != "info" {
		t.Errorf("allowlisted logging.level = %v, want kept", got["logging.level"])
	}
	fm := newRedactor(v1.DomainMemory, "memory/x.md").memoryFrontmatter(map[string]string{"ORG_NOTE": "private"})
	if fm["ORG_NOTE"] != redacted {
		t.Errorf("memory ORG_NOTE = %v, want REDACTED", fm["ORG_NOTE"])
	}
}

func TestRedactorVaultValuesAlwaysRedacted(t *testing.T) {
	r := newRedactor(v1.DomainVault, "vault/vault.env")
	changes := []v1.Change{
		{Operation: v1.OperationCreate, Source: ".claude/vault.env", Target: "MIG_FIXTURE_SECRET_2", ContentHash: "abc123"},
		{Operation: v1.OperationUpdate, Source: ".claude/vault.env", Target: "MIG_FIXTURE_SECRET_1"},
		{Operation: v1.OperationUnchanged, Source: ".claude/vault.env", Target: "MIG_FIXTURE_SECRET_2"},
	}
	got := r.dryRun(v1.DryRunResult{Changes: changes})["changes"].([]map[string]any)
	want := []string{"NAME-1", "NAME-2", "NAME-1"}
	for i, change := range got {
		if change["target"] != want[i] {
			t.Errorf("change %d target = %v, want %s", i, change["target"], want[i])
		}
		if change["content_hash"] != redacted || change["source"] != "vault/vault.env" {
			t.Errorf("change %d = %v, want REDACTED hash and the input-relative source", i, change)
		}
		if change["operation"] != changes[i].Operation {
			t.Errorf("change %d lost its operation", i)
		}
	}
	if r.mapped("free text naming MIG_FIXTURE_SECRET_1") != redacted {
		t.Error("free text passed through the name map")
	}
}

func TestRedactorMemoryUnknownFrontmatterRedacted(t *testing.T) {
	r := newRedactor(v1.DomainMemory, "memory/x.md")
	got := r.memoryFrontmatter(map[string]string{"name": "x", "commit_sha": "deadbeef", "team_secret": "private"})
	if got["name"] != "x" {
		t.Errorf("name = %v, want kept", got["name"])
	}
	for _, key := range []string{"commit_sha", "team_secret"} {
		if got[key] != redacted {
			t.Errorf("%s = %v, want REDACTED", key, got[key])
		}
	}
}

func TestRedactor_UnclassifiableKeptAsRedacted(t *testing.T) {
	r := newRedactor(v1.DomainAccounts, "accounts/accounts.json")
	got := r.provider(registry.ProviderRecord{Name: "openai.acct", AuthRef: "v1.account.acct", BaseURL: "https://x.example"})
	typ := reflect.TypeOf(registry.ProviderRecord{})
	if len(got) != typ.NumField() {
		t.Fatalf("redacted provider has %d keys, the type has %d fields", len(got), typ.NumField())
	}
	if got["BaseURL"] != redacted || got["Name"] != "ACCOUNT-1" || got["AuthRef"] != "NAME-1" {
		t.Errorf("provider = %v", got)
	}
	type extended struct {
		Kept    string
		Unknown string `json:"unknown"`
		hidden  string
	}
	out := redactStruct(extended{Kept: "k", Unknown: "u", hidden: "h"}, nil, []string{"Kept"})
	if value, present := out["unknown"]; !present || value != redacted {
		t.Errorf("unknown field = (%v, %v), want present and REDACTED", value, present)
	}
	if len(out) != 2 || out["Kept"] != "k" {
		t.Errorf("redactStruct = %v", out)
	}
	j := r.journal(v1.JournalEntry{Code: "accounts.unheard-of", Source: "x#ACCOUNT", Detail: "openai.acct"})
	if j["code"] != redacted || j["detail"] != "ACCOUNT-1" || j["source"] != "accounts/accounts.json#REDACTED" {
		t.Errorf("journal = %v", j)
	}
}

func TestRedactor_TargetsAndPaths(t *testing.T) {
	cfg := newRedactor(v1.DomainConfig, "config/x.toml")
	if cfg.target("org.note") != redacted || cfg.target("logging.level") != "logging.level" {
		t.Error("config target rule is wrong")
	}
	if cfg.source(".cascade/config.toml#org.note") != "config/x.toml#REDACTED" {
		t.Error("an unknown config source fragment was kept")
	}
	forms := append([]string{"/run/x", "~/x", `C:\x`, `\\server\share`}, socketForms...)
	for _, value := range forms {
		if got := cfg.configKeys(map[string]any{"daemon.socket": value}); got["daemon.socket"] != redactedPath {
			t.Errorf("path form %d was not REDACTED-PATH", len(value))
		}
	}
	if got := cfg.configKeys(map[string]any{"daemon.socket": "d.sock"}); got["daemon.socket"] != "d.sock" {
		t.Error("a bare socket file name was redacted")
	}
	if newRedactor(v1.Domain("other"), "x").target("t") != redacted {
		t.Error("an unknown domain kept a target")
	}
}
