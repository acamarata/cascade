package main

// Purpose: TestHarvestConfig_RefusalRecorded. The designed translator
// refusal becomes a refusal fixture; every other config error refuses the
// run. Negatives are judged by error identity and message, never by
// errors.Is (cascade errors.Is compares Kind only).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	v1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestHarvestConfig_RefusalRecorded(t *testing.T) {
	t.Run("refusal-fixture", testConfigRefusalFixture)
	t.Run("empty-destination-exits-2", testConfigEmptyDestination)
	t.Run("unmappable-value-exits-2", testConfigUnmappableValue)
}

// testConfigRefusalFixture: daemon-integration.toml yields a refusal
// fixture whose key paths equal the importer's v1_unknown_config journal.
func testConfigRefusalFixture(t *testing.T) {
	root := newModule(t)
	if code, _, stderr := runHarvest(t, "--domain", "config"); code != exitOK {
		t.Fatalf("harvest failed: %s", stderr)
	}
	var refusal map[string]any
	for path, file := range readOutputs(t, root) {
		if strings.Contains(path, "/config-refusal-") {
			if err := json.Unmarshal(file.data, &refusal); err != nil {
				t.Fatal(err)
			}
		}
	}
	if refusal == nil || refusal["input"] != "config/daemon-integration.toml" {
		t.Fatalf("no refusal fixture for daemon-integration.toml: %v", refusal)
	}
	if refusal["error"] != knownRefusalMessage {
		t.Errorf("refusal error = %v, want the importer's known refusal message", refusal["error"])
	}
	var got []string
	for _, key := range refusal["refused_key_paths"].([]any) {
		got = append(got, key.(string))
	}
	if want := journalUnknownKeys(t, inputFor(t, "config/daemon-integration.toml")); !reflect.DeepEqual(got, want) || len(want) == 0 {
		t.Errorf("refused key paths = %v, want the v1_unknown_config journal keys %v", got, want)
	}
}

// journalUnknownKeys runs the real importer and returns its
// v1_unknown_config journal key paths, sorted.
func journalUnknownKeys(t *testing.T, input inputFile) []string {
	t.Helper()
	home := t.TempDir()
	if err := stage(home, ".cascade/config.toml", input.Data); err != nil {
		t.Fatal(err)
	}
	res, err := v1.NewConfigImporter(filepath.Join(t.TempDir(), "c.toml")).Import(context.Background(), v1.Request{SourceRoot: home})
	if err == nil {
		t.Fatal("the importer accepted daemon-integration.toml")
	}
	var keys []string
	for _, entry := range res.Journal {
		if entry.Code == "v1_unknown_config" {
			_, key, _ := strings.Cut(entry.Source, "#")
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// testConfigEmptyDestination: an empty destination is KindInvalidInput with
// no journal, so it refuses the run rather than becoming a refusal fixture.
func testConfigEmptyDestination(t *testing.T) {
	root := newModule(t)
	prev := importerFor
	importerFor = func(domain v1.Domain, deps importerDeps) v1.Importer {
		if domain == v1.DomainConfig {
			deps.configDest = ""
		}
		return prev(domain, deps)
	}
	t.Cleanup(func() { importerFor = prev })
	assertRefusedNothingWritten(t, root, "the config importer refused config/daemon-integration.toml (invalid-input)")
	_, err := harvestConfig(context.Background(), t.TempDir(), inputFor(t, "config/daemon-known.toml"))
	assertImporterError(t, err, "migration v1 config: destination path is required", nil)
}

// testConfigUnmappableValue: an unmappable value wraps ErrUnknownInput with
// no journal, so it refuses the run; errors.Is would wrongly accept it.
func testConfigUnmappableValue(t *testing.T) {
	root := newModule(t)
	removeConfigInputs(t, root)
	source := []byte("[daemon]\nlog_level = \"verbose\"\n")
	writeTestFile(t, filepath.Join(pinnedOf(root), "config", "bad.toml"), source)
	rewriteManifest(t, root)
	assertRefusedNothingWritten(t, root, "the config importer refused config/bad.toml (invalid-input)")
	_, err := harvestConfig(context.Background(), t.TempDir(), inputFile{Domain: v1.DomainConfig, Rel: "config/bad.toml", Data: source})
	assertImporterError(t, err, `migration v1 config: daemon.log_level has unmappable value "verbose"`, v1.ErrUnknownInput)
}

// assertImporterError checks that err is the harvester's importer refusal
// wrapping the importer's own *cascade.Error with exactly msg (and, when
// cause is non-nil, wrapping exactly that sentinel value).
func assertImporterError(t *testing.T, err error, msg string, cause error) {
	t.Helper()
	var refusal *importRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("err %v is not an importer refusal", err)
	}
	var ce *cascade.Error
	if !errors.As(refusal.cause, &ce) {
		t.Fatalf("importer error is not a *cascade.Error")
	}
	if ce.Kind != cascade.KindInvalidInput || ce.Msg != msg {
		t.Errorf("importer error = (%v, %q), want (invalid-input, %q)", ce.Kind, ce.Msg, msg)
	}
	if ce.Err != cause {
		t.Errorf("importer error wraps %v, want exactly %v", ce.Err, cause)
	}
}

func TestReadV2ConfigKeys_Errors(t *testing.T) {
	flat, err := readV2ConfigKeys(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil || len(flat) != 0 {
		t.Fatalf("absent config = %v, %v", flat, err)
	}
	bad := filepath.Join(t.TempDir(), "bad.toml")
	writeTestFile(t, bad, []byte("[[[not toml\n"))
	if _, err := readV2ConfigKeys(bad); err == nil {
		t.Error("malformed v2 config accepted")
	}
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readV2ConfigKeys(dir); err == nil {
		t.Error("an unreadable v2 config accepted")
	}
}
