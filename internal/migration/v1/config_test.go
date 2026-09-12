package v1

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestConfigTranslator_GoldenKnownKeys(t *testing.T) {
	root := stageFixture(t, "config/daemon-known.toml", ".cascade/config.toml")
	destination := filepath.Join(t.TempDir(), "config.toml")
	result, err := NewConfigImporter(destination).Import(context.Background(), Request{SourceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeltaCount() != 2 || !result.Applied {
		t.Fatalf("unexpected result: %+v", result)
	}
	tree := readConfigTree(t, destination)
	if value, _ := dottedValue(tree, "logging.level"); value != "info" {
		t.Fatalf("logging.level = %#v", value)
	}
	if value, _ := dottedValue(tree, "schema_version"); value != int64(runtime.CurrentSchemaVersion) {
		t.Fatalf("schema_version = %#v", value)
	}
}

func TestConfigTranslator_UnknownKeysQuarantined(t *testing.T) {
	root := stageFixture(t, "config/daemon-integration.toml", ".cascade/config.toml")
	destination := filepath.Join(t.TempDir(), "config.toml")
	original := []byte("schema_version = 1\n[logging]\nlevel = \"warn\"\n")
	if err := os.WriteFile(destination, original, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := NewConfigImporter(destination).Import(context.Background(), Request{SourceRoot: root})
	assertKind(t, err, cascade.KindInvalidInput)
	assertSentinel(t, err, ErrUnknownInput)
	if len(result.Journal) != 5 {
		t.Fatalf("quarantined %d keys, want 5: %+v", len(result.Journal), result.Journal)
	}
	for _, entry := range result.Journal {
		if entry.Code != "v1_unknown_config" || !strings.Contains(entry.Detail, "no destination") {
			t.Fatalf("bad quarantine entry: %+v", entry)
		}
	}
	if got, readErr := os.ReadFile(destination); readErr != nil || !bytes.Equal(got, original) {
		t.Fatalf("unknown-key refusal changed destination: %q err=%v", got, readErr)
	}
}

func TestConfigTranslator_MapsVerifiedKeys(t *testing.T) {
	root := t.TempDir()
	source := []byte("schema_version = 1\n[daemon]\nlog_level = \"debug\"\nlog_format = \"pretty\"\nsocket_path = \"/tmp/v1.sock\"\n[telemetry]\nenabled = false\n")
	writeSource(t, root, ".cascade/config.toml", source)
	destination := filepath.Join(t.TempDir(), "config.toml")
	result, err := NewConfigImporter(destination).Import(context.Background(), Request{SourceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeltaCount() != 5 {
		t.Fatalf("delta = %+v", result)
	}
	tree := readConfigTree(t, destination)
	assertDottedConfig(t, tree, "logging.level", "debug")
	assertDottedConfig(t, tree, "logging.format", "text")
	assertDottedConfig(t, tree, "daemon.socket", "/tmp/v1.sock")
	assertDottedConfig(t, tree, "telemetry.enabled", false)
}

func TestConfigTranslator_ConflictRefusesVisibly(t *testing.T) {
	root := stageFixture(t, "config/daemon-known.toml", ".cascade/config.toml")
	destination := filepath.Join(t.TempDir(), "config.toml")
	original := []byte("schema_version = 1\n[logging]\nlevel = \"error\"\n")
	if err := os.WriteFile(destination, original, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := NewConfigImporter(destination).Import(context.Background(), Request{SourceRoot: root})
	assertKind(t, err, cascade.KindConflict)
	assertSentinel(t, err, ErrImportConflict)
	if len(result.Changes) != 0 {
		t.Fatalf("failed import returned a partial plan: %+v", result)
	}
	if got, _ := os.ReadFile(destination); !bytes.Equal(got, original) {
		t.Fatalf("existing config changed: %q", got)
	}
}

func TestConfigTranslator_DryRunDoesNotWrite(t *testing.T) {
	root := stageFixture(t, "config/daemon-known.toml", ".cascade/config.toml")
	destination := filepath.Join(t.TempDir(), "config.toml")
	result, err := NewConfigImporter(destination).Import(context.Background(), Request{SourceRoot: root, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.DeltaCount() != 2 {
		t.Fatalf("unexpected dry run: %+v", result)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote destination: %v", err)
	}
}

func TestConfigTranslator_FailClosedSchemaAndValues(t *testing.T) {
	cases := []struct {
		name string
		data string
		kind cascade.Kind
	}{
		{"malformed", "[daemon\n", cascade.KindIntegrity},
		{"schema type", "schema_version = \"1\"\n", cascade.KindIntegrity},
		{"future", "schema_version = 2\n", cascade.KindUnsupported},
		{"log type", "[daemon]\nlog_level = 4\n", cascade.KindIntegrity},
		{"unknown level", "[daemon]\nlog_level = \"trace\"\n", cascade.KindInvalidInput},
		{"format type", "[daemon]\nlog_format = false\n", cascade.KindIntegrity},
		{"unknown format", "[daemon]\nlog_format = \"compact\"\n", cascade.KindInvalidInput},
		{"socket empty", "[daemon]\nsocket_path = \" \"\n", cascade.KindIntegrity},
		{"telemetry type", "[telemetry]\nenabled = \"yes\"\n", cascade.KindIntegrity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := translateV1Config([]byte(tc.data))
			assertKind(t, err, tc.kind)
		})
	}
}

func TestConfigTranslator_EmptyUnknownTableRefuses(t *testing.T) {
	_, unknown, err := translateV1Config([]byte("[budget]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown) != 1 || unknown[0] != "budget" {
		t.Fatalf("empty table silently dropped: %v", unknown)
	}
}

func TestConfigTranslator_InvalidDestinationDoesNotChange(t *testing.T) {
	root := stageFixture(t, "config/daemon-known.toml", ".cascade/config.toml")
	destination := filepath.Join(t.TempDir(), "config.toml")
	original := []byte("[logging\n")
	if err := os.WriteFile(destination, original, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewConfigImporter(destination).Import(context.Background(), Request{SourceRoot: root})
	assertKind(t, err, cascade.KindIntegrity)
	if got, _ := os.ReadFile(destination); !bytes.Equal(got, original) {
		t.Fatal("invalid destination changed")
	}
}

func TestConfigTranslator_SourceErrorsAreTyped(t *testing.T) {
	_, _, err := readV1Config("")
	assertKind(t, err, cascade.KindInvalidInput)
	_, _, err = readV1Config(t.TempDir())
	assertKind(t, err, cascade.KindNotFound)
	_, err = NewConfigImporter("").Import(context.Background(), Request{SourceRoot: t.TempDir()})
	assertKind(t, err, cascade.KindInvalidInput)

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".cascade", "config.toml"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, err = readV1Config(root)
	assertKind(t, err, cascade.KindInvalidInput)
}

func TestConfigTranslator_ValidateBeforeWrite(t *testing.T) {
	root := stageFixture(t, "config/daemon-known.toml", ".cascade/config.toml")
	destination := filepath.Join(t.TempDir(), "config.toml")
	original := []byte("schema_version = 1\n[logging]\nformat = 42\n")
	if err := os.WriteFile(destination, original, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewConfigImporter(destination).Import(context.Background(), Request{SourceRoot: root})
	assertKind(t, err, cascade.KindInvalidInput)
	if got, _ := os.ReadFile(destination); !bytes.Equal(got, original) {
		t.Fatal("validation failure changed destination")
	}
}

func readConfigTree(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tree := map[string]interface{}{}
	if err := toml.Unmarshal(data, &tree); err != nil {
		t.Fatal(err)
	}
	return tree
}

func assertDottedConfig(t *testing.T, tree map[string]interface{}, key string, want interface{}) {
	t.Helper()
	got, ok := dottedValue(tree, key)
	if !ok || got != want {
		t.Fatalf("%s = %#v, want %#v", key, got, want)
	}
}
