package update

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestCheckIsReportOnly is acceptance [4].
func TestCheckIsReportOnly(t *testing.T) {
	f := newFixture(t, "2.0.0", "2.0.1")
	before := treeSum(t, f.home)
	res, err := Check(context.Background(), f.deps("2.0.0", nil))
	must(t, err)
	if res.Action != ActionUpdate || res.Latest.Version != "2.0.1" || res.Current.ReleaseTag != "v2.0.0" {
		t.Fatalf("check = %+v", res)
	}
	if after := treeSum(t, f.home); after != before {
		t.Fatal("--check wrote to the home tree")
	}
	raw, err := json.Marshal(res)
	must(t, err)
	var generic map[string]any
	must(t, json.Unmarshal(raw, &generic))
	if v, ok := generic["compatibility"]; !ok || v != nil {
		t.Fatalf("compatibility = %v (present %v), want null", v, ok)
	}
	var back CheckResult
	must(t, json.Unmarshal(raw, &back))
	if back.Action != res.Action || back.Latest != res.Latest || back.Compatibility != nil {
		t.Fatalf("round trip = %+v", back)
	}
}

// TestApplyNoOpWhenArchiveDigestMatches is acceptance [5].
func TestApplyNoOpWhenArchiveDigestMatches(t *testing.T) {
	f := newFixture(t, "2.0.0", "2.0.1")
	r, err := ReadReceipt(f.receipt)
	must(t, err)
	r.ArchiveSHA256 = f.archiveSHA
	must(t, WriteReceipt(f.receipt, r))
	before := treeSum(t, f.home)
	cf := &countingFetcher{Fetcher: DirFetcher{Dir: f.dist}}
	res, err := Apply(context.Background(), f.deps("2.0.0", cf))
	must(t, err)
	if res.Action != ActionNone || res.Updated {
		t.Fatalf("apply = %+v, want action none", res)
	}
	for _, n := range cf.names() {
		if n == f.archiveName {
			t.Fatal("the archive was downloaded although its digest matches the receipt")
		}
	}
	if treeSum(t, f.home) != before {
		t.Fatal("a digest-match no-op wrote to the home tree")
	}
}

// TestApplyEqualVersionIsNone is acceptance [11] (equal half).
func TestApplyEqualVersionIsNone(t *testing.T) {
	f := newFixture(t, "2.0.1", "2.0.1")
	before := treeSum(t, f.home)
	res, err := Apply(context.Background(), f.deps("2.0.1", nil))
	must(t, err)
	if res.Action != ActionNone || res.Updated {
		t.Fatalf("apply = %+v, want none", res)
	}
	if treeSum(t, f.home) != before {
		t.Fatal("an equal-version no-op wrote to the home tree")
	}
}

// TestFetchVerifiedRefusesSignedOlderRelease is acceptance [11]: an older
// validly signed set refuses with "downgrade" and changes nothing; an rc
// below its final refuses; a final above an rc updates.
func TestFetchVerifiedRefusesSignedOlderRelease(t *testing.T) {
	// The last case installs an older receipt than the running binary, so
	// only the running-version comparison can refuse it.
	for _, tc := range []struct{ installed, running, served string }{
		{"2.0.1", "2.0.1", "2.0.0"}, {"2.0.0", "2.0.0", "2.0.0-rc.1"}, {"1.0.0", "2.0.1", "2.0.0"},
	} {
		f := newFixture(t, tc.installed, tc.served)
		before := treeSum(t, f.home)
		_, err := Apply(context.Background(), f.deps(tc.running, nil))
		wantKind(t, err, cascade.KindIntegrity, "downgrade")
		if treeSum(t, f.home) != before {
			t.Fatalf("%s served to %s wrote to the home tree", tc.served, tc.running)
		}
	}
	f := newFixture(t, "2.0.0-rc.1", "2.0.1")
	res, err := Apply(context.Background(), f.deps("2.0.0-rc.1", nil))
	must(t, err)
	if !res.Updated || f.read("cascade") != "cascade 2.0.1" {
		t.Fatalf("2.0.1 served to 2.0.0-rc.1 did not update: %+v", res)
	}
}

// TestApplyRefusesMixedInstall is acceptance [16].
func TestApplyRefusesMixedInstall(t *testing.T) {
	f := newFixture(t, "2.0.0", "2.0.1")
	put(t, f.installDir, "cascade-elevate-helper", []byte("other helper"))
	put(t, f.installDir, "cascade-github", []byte("other plugin"))
	before := treeSum(t, f.home)
	cf := &countingFetcher{Fetcher: DirFetcher{Dir: f.dist}}
	for _, run := range []func(context.Context, Deps) error{
		func(ctx context.Context, d Deps) error { _, err := Check(ctx, d); return err },
		func(ctx context.Context, d Deps) error { _, err := Apply(ctx, d); return err },
	} {
		err := run(context.Background(), f.deps("2.0.0", cf))
		wantKind(t, err, cascade.KindIntegrity, "install differs from receipt")
		if !strings.Contains(err.Error(), "cascade-elevate-helper") || !strings.Contains(err.Error(), "rerun install.sh") {
			t.Fatalf("refusal does not name the file and the remedy: %v", err)
		}
	}
	if len(cf.names()) != 0 || treeSum(t, f.home) != before {
		t.Fatalf("a mixed install fetched %v or wrote", cf.names())
	}
	clean := newFixture(t, "2.0.0", "2.0.1")
	if res, err := Apply(context.Background(), clean.deps("2.0.0", nil)); err != nil || !res.Updated {
		t.Fatalf("a clean install did not proceed: %+v, %v", res, err)
	}
}

// TestApplyUpdatesWithoutDaemon covers the no-daemon path end to end.
func TestApplyUpdatesWithoutDaemon(t *testing.T) {
	f := newFixture(t, "2.0.0", "2.0.1")
	res, err := Apply(context.Background(), f.deps("2.0.0", nil))
	must(t, err)
	if !res.Updated || res.DaemonSignalled || f.read("cascade") != "cascade 2.0.1" || f.read("cascade-github") != "github 2.0.1" {
		t.Fatalf("apply = %+v", res)
	}
	r, err := ReadReceipt(f.receipt)
	must(t, err)
	if r.ReleaseTag != "v2.0.1" || r.ArchiveSHA256 != f.archiveSHA || r.InstalledAt != "2026-10-19T10:00:00Z" {
		t.Fatalf("receipt = %+v", r)
	}
	prev, err := ReadReceipt(filepath.Join(PreviousDir(f.dataDir), "receipt.json"))
	must(t, err)
	if prev.ReleaseTag != "v2.0.0" {
		t.Fatalf("previous receipt = %+v", prev)
	}
	b, err := os.ReadFile(filepath.Join(PreviousDir(f.dataDir), "cascade"))
	must(t, err)
	if string(b) != "cascade 2.0.0" {
		t.Fatalf("previous cascade = %q", b)
	}
	if again, err := Apply(context.Background(), f.deps("2.0.1", nil)); err != nil || again.Action != ActionNone {
		t.Fatalf("second run = %+v, %v, want none", again, err)
	}
}

// TestApplyChannelNoops covers brew, oci, node-managed, manual and dev.
func TestApplyChannelNoops(t *testing.T) {
	f := newFixture(t, "2.0.0", "2.0.1")
	before := treeSum(t, f.home)
	for stamp, want := range map[string]Action{"brew": ActionNoopChannel, "oci": ActionNoopChannel, "node-managed": ActionDeferController} {
		d := f.deps("2.0.0", &countingFetcher{fail: os.ErrInvalid})
		d.Env.Stamp = stamp
		res, err := Apply(context.Background(), d)
		if err != nil || res.Action != want {
			t.Fatalf("stamp %s = %+v, %v; want %s", stamp, res, err, want)
		}
	}
	d := f.deps("2.0.0", nil)
	d.Env.ReceiptPath = filepath.Join(f.home, "none.json")
	if res, err := Apply(context.Background(), d); err != nil || res.Channel != ChannelManual || res.Action != ActionNoopChannel {
		t.Fatalf("manual = %+v, %v", res, err)
	}
	dev := f.deps("dev", nil)
	dev.Source = Source{BaseURL: DefaultBaseURL}
	_, err := Apply(context.Background(), dev)
	wantKind(t, err, cascade.KindUnsupported, "dev build")
	if treeSum(t, f.home) != before {
		t.Fatal("a no-op channel wrote to the home tree")
	}
	put(t, filepath.Dir(f.receipt), "broken.json", []byte(`{"schema_version":2}`))
	d.Env.ReceiptPath = filepath.Join(filepath.Dir(f.receipt), "broken.json")
	_, err = Check(context.Background(), d)
	wantKind(t, err, cascade.KindIntegrity, "schema_version")
}
