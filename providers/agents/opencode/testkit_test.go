package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Unix(1, 0) }

type replayOptions struct {
	Lines      string
	Exit       int
	Block      bool
	Stderr     string
	Descendant bool
}

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "hold-pipe" {
		<-time.NewTimer(time.Hour).C
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "run" {
		replay()
		return
	}
	os.Exit(m.Run())
}

func replay() {
	if os.Getenv("OPENCODE_PARENT_CANARY") != "" {
		os.Exit(94)
	}
	b, err := os.ReadFile(filepath.Join(os.Getenv(VendorAuthVar), "replay.json"))
	if err != nil {
		os.Exit(90)
	}
	var o replayOptions
	if json.Unmarshal(b, &o) != nil {
		os.Exit(91)
	}
	if o.Descendant {
		bin, err := os.Executable()
		if err != nil {
			os.Exit(92)
		}
		_, err = os.StartProcess(bin, []string{bin, "hold-pipe"}, &os.ProcAttr{Env: []string{"HOME=" + os.Getenv("HOME")}, Files: []*os.File{nil, os.Stdout, os.Stderr}})
		if err != nil {
			os.Exit(93)
		}
	}
	_, _ = fmt.Fprint(os.Stdout, o.Lines)
	fmt.Fprint(os.Stderr, o.Stderr)
	if o.Block {
		<-time.NewTimer(time.Hour).C
	}
	os.Exit(o.Exit)
}

type kit struct {
	t          *testing.T
	root, home string
	cfg        Config
	env        provider.SpawnEnv
	opts       replayOptions
	d          *Driver
}

func newKit(t *testing.T) *kit {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("native process groups require unix")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("testdata/run.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	k := &kit{t: t, root: t.TempDir(), home: home, opts: replayOptions{Lines: string(b)}}
	k.env = provider.SpawnEnv{Home: home, Driver: []string{"HOME=" + home, VendorAuthVar + "=" + home, KeyVarCompatKey + "=fixture"}}
	k.cfg = Config{Binary: bin, RuntimeHome: home, Clock: fixedClock{}, Posture: provider.CompliancePosture{ProgrammaticEntitlement: true}, Defaults: provider.DriverDefaults{CancelGrace: time.Millisecond}}
	return k
}

func identity(b []byte) ([]byte, error) { return b, nil }

func (k *kit) driver() *Driver {
	k.t.Helper()
	b, _ := json.Marshal(k.opts)
	if err := os.WriteFile(filepath.Join(k.home, "replay.json"), b, 0600); err != nil {
		k.t.Fatal(err)
	}
	if k.d != nil {
		return k.d
	}
	d, err := New(k.cfg)
	if err != nil {
		k.t.Fatal(err)
	}
	k.d = d
	k.t.Cleanup(func() {
		for id := range d.jobs {
			if err := d.Cancel(context.Background(), id); err != nil {
				k.t.Error(err)
			}
		}
	})
	return d
}

func (k *kit) spec() provider.AgentJobSpec {
	return provider.AgentJobSpec{Prompt: "reply ready", Env: k.env, WorktreeRoot: k.root, Filter: identity, DataClass: provider.DataClassInternal, Envelope: provider.SpawnEnvelope{Digest: strings.Repeat("ab", 32), Principal: "fixture", Roots: []string{k.root}, ExpiresAt: time.Unix(100000, 0), Sensitivity: provider.SensitivityInternal}}
}

func (k *kit) run(s provider.AgentJobSpec) (provider.SpawnResult, provider.CollectResult, error) {
	k.t.Helper()
	d := k.driver()
	r, err := d.Spawn(context.Background(), s)
	if err != nil {
		k.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := d.Collect(ctx, r.JobID)
	return r, c, err
}

func wantError(t *testing.T, got, want error) {
	t.Helper()
	if got != want || got.Error() != want.Error() {
		t.Fatalf("got %v; want %v by identity and message", got, want)
	}
}
