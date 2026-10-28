package uninstall

// Purpose: the refusal and error edges of every item, so each failure is
//   a typed error with the item kept, never a silent removal.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// brokenPlugins fails List or Remove.
type brokenPlugins struct{ listErr, removeErr error }

func (b brokenPlugins) List(context.Context) ([]string, error) {
	return []string{"fake-plugin"}, b.listErr
}
func (b brokenPlugins) Remove(context.Context, plugins.ProcessTeardown, string) error {
	return b.removeErr
}

// brokenCustody fails List.
type brokenCustody struct{ secrets.Custody }

func (brokenCustody) List(context.Context) ([]string, error) {
	return nil, cascade.New(cascade.KindUnavailable, "fake: locked keychain")
}

func TestDataItemsRefuseNonRegularLocators(t *testing.T) {
	f := newFixture(t, "linux")
	_ = os.Remove(f.paths.ConfigPath())
	mustMkdir(t, f.paths.ConfigPath())
	cfg := fileItem{kind: KindConfig, locator: f.paths.ConfigPath(), d: f.deps}
	if d, err := cfg.Remove(t.Context(), false); err == nil || d.Action != ActionKept || !exists(f.paths.ConfigPath()) {
		t.Fatalf("directory at the config path = %+v, %v", d, err)
	}
	notDir := filepath.Join(f.top, "data-file")
	mustWrite(t, notDir, "x")
	dd := dataDirItem{fileItem{kind: KindDataDir, locator: notDir, d: f.deps}}
	if d, err := dd.Remove(t.Context(), false); err == nil || d.Action != ActionKept || !exists(notDir) {
		t.Fatalf("file at the data dir path = %+v, %v", d, err)
	}
	gone := dataDirItem{fileItem{kind: KindDataDir, locator: filepath.Join(f.top, "absent"), d: f.deps}}
	if d, err := gone.Remove(t.Context(), false); err != nil || d.Action != ActionAbsent {
		t.Fatalf("absent data dir = %+v, %v", d, err)
	}
	rel := fileItem{kind: KindConfig, locator: "config.toml", d: f.deps}
	if d, err := rel.Remove(t.Context(), true); err == nil || d.Action != ActionKept {
		t.Fatalf("relative locator = %+v, %v", d, err)
	}
}

func TestElevationKeyItemPaths(t *testing.T) {
	f := newFixture(t, "linux")
	key := filepath.Join(f.paths.DataDir(), elevationKeyFile)
	mustWrite(t, key, "fake key\n")
	f.deps.HelperDelete = func(context.Context) error {
		return cascade.New(cascade.KindPermissionDenied, "fake: presence refused")
	}
	item := keyItem{fileItem{kind: KindElevationKey, locator: key, d: f.deps}}
	if d, err := item.Remove(t.Context(), true); err != nil || d.Action != ActionWouldRemove || !exists(key) {
		t.Fatalf("dry run = %+v, %v", d, err)
	}
	if d, err := item.Remove(t.Context(), false); err == nil || d.Action != ActionKept {
		t.Fatalf("refused presence = %+v, %v", d, err)
	}
	f.deps.HelperDelete = nil
	item = keyItem{fileItem{kind: KindElevationKey, locator: key, d: f.deps}}
	if d, err := item.Remove(t.Context(), false); err != nil || d.Action != ActionKept || !strings.Contains(d.Note, "not removed by this build") {
		t.Fatalf("unwired helper = %+v, %v", d, err)
	}
}

func TestReceiptItemEdges(t *testing.T) {
	f := newFixture(t, "linux")
	path := filepath.Join(f.paths.Root(), receiptFileName)
	_ = os.Remove(path)
	mustMkdir(t, path)
	r := receiptItem{plan: buildExecPlan(f.deps), d: f.deps, locator: path}
	if d, err := r.Remove(t.Context(), false); err == nil || d.Action != ActionKept {
		t.Fatalf("directory receipt = %+v, %v", d, err)
	}
	f.receipt.Channel = "oci"
	_ = os.Remove(path)
	mustWrite(t, path, "{}\n")
	rep, err := Run(t.Context(), mustInventory(t, f.deps), Options{})
	if err != nil || deltaFor(t, rep, path).Action != ActionRemoved {
		t.Fatalf("oci receipt = %+v, %v", rep, err)
	}
	if d := deltaFor(t, rep, filepath.Join(f.installDir, "cascade")); d.Note != channelRemedy["oci"] {
		t.Fatalf("oci binary = %+v", d)
	}
}

func TestInventoryAndItemErrorsAreTyped(t *testing.T) {
	f := newFixture(t, "linux")
	if _, err := Inventory(t.Context(), Deps{}); err == nil {
		t.Fatal("Inventory without paths succeeded")
	}
	d := f.deps
	d.Plugins = brokenPlugins{listErr: cascade.New(cascade.KindUnavailable, "fake: store locked")}
	if _, err := Inventory(t.Context(), d); err == nil {
		t.Fatal("a failed plugin listing was ignored")
	}
	d = f.deps
	d.Custody = func() (secrets.Custody, error) { return nil, errors.New("fake: no custody") }
	if _, err := Inventory(t.Context(), d); err == nil {
		t.Fatal("a failed custody selection was ignored")
	}
	d.Custody = func() (secrets.Custody, error) { return brokenCustody{f.custody}, nil }
	if _, err := Inventory(t.Context(), d); err == nil {
		t.Fatal("a failed vault listing was ignored")
	}
	if dl, err := (vaultItem{name: "FAKE_TOKEN_A", custody: brokenCustody{f.custody}}).Remove(t.Context(), false); err == nil || dl.Action != ActionKept {
		t.Fatalf("vault remove over a failing list = %+v, %v", dl, err)
	}
	p := pluginItem{name: "fake-plugin", d: Deps{Plugins: brokenPlugins{removeErr: cascade.New(cascade.KindUnavailable, "fake")}}}
	if dl, err := p.Remove(t.Context(), false); err == nil || dl.Action != ActionKept || p.Owner() != "fake-plugin" {
		t.Fatalf("failing plugin teardown = %+v, %v", dl, err)
	}
	p.d.Plugins = brokenPlugins{removeErr: cascade.New(cascade.KindNotFound, "fake: gone")}
	if dl, err := p.Remove(t.Context(), false); err != nil || dl.Action != ActionAbsent {
		t.Fatalf("already-removed plugin = %+v, %v", dl, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Run(ctx, mustInventory(t, f.deps), Options{}); err == nil || kindOr(err, cascade.KindInternal) != cascade.KindCanceled {
		t.Fatalf("canceled run = %v", err)
	}
	if kindOr(errors.New("plain"), cascade.KindInternal) != cascade.KindInternal {
		t.Fatal("kindOr default")
	}
}

func TestReceiptPlanRefusals(t *testing.T) {
	f := newFixture(t, "linux")
	f.deps.Executable = func() (string, error) { return "", errors.New("fake: no executable") }
	if p := buildExecPlan(f.deps); p.refused == nil {
		t.Fatal("an unresolvable running executable was accepted")
	}
	f.deps.Executable = nil
	if p := buildExecPlan(f.deps); p.refused == nil || p.running != "" {
		t.Fatal("a missing executable seam was accepted")
	}
	f = newFixture(t, "linux")
	f.receipt.Files[0].SHA256 = strings.ToUpper(f.receipt.Files[0].SHA256)
	if p := buildExecPlan(f.deps); p.refused == nil {
		t.Fatal("an upper-case digest was accepted")
	}
	f.deps.Receipt = nil
	if p := buildExecPlan(f.deps); p.present || p.refused != nil {
		t.Fatalf("no receipt seam = %+v", p)
	}
	if receiptPath(Deps{}) != "" {
		t.Fatal("receiptPath without paths")
	}
	if (keptItem{}).Owner() != "cascade" {
		t.Fatal("kept item owner")
	}
}

func TestReceiptKeptOnARealRunWhileAListedFileRemains(t *testing.T) {
	f := newFixture(t, "linux")
	path := filepath.Join(f.paths.Root(), receiptFileName)
	r := receiptItem{plan: buildExecPlan(f.deps), d: f.deps, locator: path}
	if d, err := r.Remove(t.Context(), true); err != nil || d.Action != ActionWouldRemove {
		t.Fatalf("dry run over matching files = %+v, %v", d, err)
	}
	// A real run reaching the receipt while a matching executable is still
	// present means that executable was kept: the receipt stays its record.
	if d, err := r.Remove(t.Context(), false); err != nil || d.Action != ActionKept || !exists(path) {
		t.Fatalf("real run with a listed file present = %+v, %v", d, err)
	}
}

func TestVaultDeleteCustodyMustBeTheListedBackend(t *testing.T) {
	for name, sel := range map[string]func(*fixture, *int) func() (secrets.Custody, error){
		"other backend": func(_ *fixture, n *int) func() (secrets.Custody, error) {
			return func() (secrets.Custody, error) { *n++; return newMemCustody("fake-other-backend"), nil }
		},
		"selection error": func(_ *fixture, n *int) func() (secrets.Custody, error) {
			return func() (secrets.Custody, error) { *n++; return nil, errors.New("fake: no keychain") }
		},
		"not-found selection": func(_ *fixture, n *int) func() (secrets.Custody, error) {
			return func() (secrets.Custody, error) {
				*n++
				return nil, cascade.New(cascade.KindNotFound, "fake: no backend")
			}
		},
		"nil selection": func(_ *fixture, n *int) func() (secrets.Custody, error) {
			return func() (secrets.Custody, error) { *n++; return nil, nil }
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "linux")
			calls := 0
			f.deps.CustodyForDelete = sel(f, &calls)
			rep, err := Run(t.Context(), mustInventory(t, f.deps), Options{})
			if k, _ := cascade.KindOf(err); err == nil || k != cascade.KindUnavailable || calls != 1 {
				t.Fatalf("run = %v (kind %v), selections %d", err, k, calls)
			}
			for _, n := range []string{"FAKE_TOKEN_A", "FAKE_TOKEN_B"} {
				if d := deltaFor(t, rep, "vault:"+n); d.Action != ActionKept {
					t.Fatalf("%s = %+v; want kept", n, d)
				}
			}
			if left, _ := f.custody.List(t.Context()); len(left) != 2 {
				t.Fatalf("listed vault after a refused delete = %v", left)
			}
		})
	}
}
