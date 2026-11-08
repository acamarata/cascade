package init

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/internal/runtime/initconfig"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeLAN is the injected discovery source; it counts every scan.
type fakeLAN struct {
	hosts []string
	scans int
}

func (l *fakeLAN) Scan(context.Context) ([]string, error) {
	l.scans++
	return append([]string(nil), l.hosts...), nil
}

// fakePairer records every pairing write and lists what it holds.
type fakePairer struct {
	paired []string
	calls  []string
	lists  int
}

func (p *fakePairer) Paired(context.Context) ([]string, error) {
	p.lists++
	return append([]string(nil), p.paired...), nil
}

func (p *fakePairer) Pair(_ context.Context, host, code string, travel bool) error {
	p.calls = append(p.calls, fmt.Sprintf("%s=%s travel=%t", host, code, travel))
	p.paired = append(p.paired, host)
	return nil
}

// isolateHome points HOME and USERPROFILE at fresh temp dirs.
func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
}

// TestRunNodesStepCodeFromEnvOnly proves a non-interactive run takes a
// paired host's code from CASCADE_INIT_NODE_CODE_<HOST> only, and that a
// missing code refuses before any pairing write or LAN scan.
func TestRunNodesStepCodeFromEnvOnly(t *testing.T) {
	isolateHome(t)
	prompt := &scriptedPrompter{lines: []string{"from-the-prompt"}}
	w, rec, _, _ := fixture(t, Options{Mode: ModeYes}, prompt)
	rec.env[initconfig.NodeCodeEnv("a.invalid")] = "code-a"
	lan, pairer := &fakeLAN{}, &fakePairer{}
	cfg := initconfig.NodesConfig{Pair: []string{"a.invalid", "b.invalid"}}

	_, err := w.StepNodes(context.Background(), cfg, lan, pairer)
	assertTaxonomyErr(t, err, cascade.KindInvalidInput, fmt.Sprintf(nodesNoCodeMsg,
		"b.invalid", initconfig.NodeCodeEnv("b.invalid")))
	if len(pairer.calls) != 0 {
		t.Fatalf("a refused run paired %v", pairer.calls)
	}
	if lan.scans != 0 || len(prompt.asked) != 0 {
		t.Fatalf("scans = %d, asked = %v; want none", lan.scans, prompt.asked)
	}

	rec.env[initconfig.NodeCodeEnv("b.invalid")] = "code-b"
	res, err := w.StepNodes(context.Background(), cfg, lan, pairer)
	if err != nil {
		t.Fatalf("StepNodes: %v", err)
	}
	want := []string{"a.invalid=code-a travel=false", "b.invalid=code-b travel=false"}
	if !reflect.DeepEqual(pairer.calls, want) || len(prompt.asked) != 0 {
		t.Fatalf("calls = %v asked = %v, want %v and no prompt", pairer.calls, prompt.asked, want)
	}
	if !reflect.DeepEqual(res.Paired, []string{"a.invalid", "b.invalid"}) {
		t.Fatalf("Paired = %v", res.Paired)
	}
}

// nodesCase is one row of TestRunNodesStep.
type nodesCase struct {
	name      string
	mode      Mode
	cfg       initconfig.NodesConfig
	env       map[string]string
	prompt    *scriptedPrompter
	lanHosts  []string
	paired    []string
	wantScans int
	wantCalls []string
	wantRes   NodesResult
}

// TestRunNodesStep drives the step's branches: nothing asked, scan off,
// scan on (interactive accept, headless list-only), prompt codes, travel,
// replay of an already-paired host and --check.
func TestRunNodesStep(t *testing.T) {
	codeA := map[string]string{initconfig.NodeCodeEnv("a.invalid"): "code-a"}
	cases := []nodesCase{
		{name: "nothing requested", mode: ModeYes, prompt: &scriptedPrompter{}},
		{name: "no scan unless opted in", mode: ModeYes, env: codeA, prompt: &scriptedPrompter{},
			cfg: initconfig.NodesConfig{Pair: []string{"a.invalid"}}, lanHosts: []string{"lan.invalid"},
			wantCalls: []string{"a.invalid=code-a travel=false"},
			wantRes:   NodesResult{Paired: []string{"a.invalid"}}},
		{name: "headless scan lists only", mode: ModeYes, prompt: &scriptedPrompter{},
			cfg: initconfig.NodesConfig{ScanLAN: true}, lanHosts: []string{"lan.invalid"}, wantScans: 1,
			wantRes: NodesResult{Discovered: []string{"lan.invalid"}}},
		{name: "interactive scan pairs with prompt code", mode: ModeInteractive,
			prompt: &scriptedPrompter{confirms: []bool{true, false}, lines: []string{"code-lan"}},
			cfg:    initconfig.NodesConfig{ScanLAN: true, Travel: true}, lanHosts: []string{"lan.invalid", "no.invalid"},
			wantScans: 1, wantCalls: []string{"lan.invalid=code-lan travel=true"},
			wantRes: NodesResult{Paired: []string{"lan.invalid"}, Travel: true,
				Discovered: []string{"lan.invalid", "no.invalid"}}},
		{name: "replay skips a paired host", mode: ModeYes, prompt: &scriptedPrompter{},
			cfg: initconfig.NodesConfig{Pair: []string{"a.invalid"}}, paired: []string{"a.invalid"},
			wantRes: NodesResult{Existing: []string{"a.invalid"}}},
		{name: "check plans without pairing", mode: ModeCheck, env: codeA, prompt: &scriptedPrompter{},
			cfg:     initconfig.NodesConfig{Pair: []string{"a.invalid"}},
			wantRes: NodesResult{Paired: []string{"a.invalid"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runNodesCase(t, tc) })
	}
}

// runNodesCase runs one TestRunNodesStep row.
func runNodesCase(t *testing.T, tc nodesCase) {
	isolateHome(t)
	w, rec, _, _ := fixture(t, Options{Mode: tc.mode}, tc.prompt)
	for k, v := range tc.env {
		rec.env[k] = v
	}
	lan := &fakeLAN{hosts: tc.lanHosts}
	pairer := &fakePairer{paired: tc.paired}
	res, err := w.StepNodes(context.Background(), tc.cfg, lan, pairer)
	if err != nil {
		t.Fatalf("StepNodes: %v", err)
	}
	if lan.scans != tc.wantScans {
		t.Fatalf("scans = %d, want %d", lan.scans, tc.wantScans)
	}
	if !reflect.DeepEqual(pairer.calls, tc.wantCalls) {
		t.Fatalf("pair calls = %v, want %v", pairer.calls, tc.wantCalls)
	}
	if !reflect.DeepEqual(res, tc.wantRes) {
		t.Fatalf("result = %+v, want %+v", res, tc.wantRes)
	}
	if len(tc.prompt.confirms) != 0 || len(tc.prompt.lines) != 0 {
		t.Fatalf("unused answers left: %+v", tc.prompt)
	}
}

// TestInitV2WindowsNodePairRefusal proves windows refuses pairing with a
// typed tier-2 error before any discovery call, while roots and policy
// still converge headless.
func TestInitV2WindowsNodePairRefusal(t *testing.T) {
	isolateHome(t)
	prompt := &scriptedPrompter{}
	w, rec, _, _ := fixture(t, Options{Mode: ModeYes}, prompt)
	w.deps.GOOS = "windows"
	rec.env[initconfig.NodeCodeEnv("a.invalid")] = "code-a"
	lan, pairer := &fakeLAN{hosts: []string{"lan.invalid"}}, &fakePairer{}
	cfg := initconfig.NodesConfig{ScanLAN: true, Pair: []string{"a.invalid"}}

	_, err := w.StepNodes(context.Background(), cfg, lan, pairer)
	assertTaxonomyErr(t, err, cascade.KindUnsupported, windowsNodesHint)
	if lan.scans != 0 || pairer.lists != 0 || len(pairer.calls) != 0 || len(prompt.asked) != 0 {
		t.Fatalf("windows reached discovery: scans %d lists %d calls %v asked %v",
			lan.scans, pairer.lists, pairer.calls, prompt.asked)
	}

	store := &fakeDiscoveryStore{}
	delta, err := ReconvergeDiscovery(context.Background(), store,
		DiscoveryState{Roots: []string{"/r/one"}, Policy: "strict"})
	if err != nil {
		t.Fatalf("ReconvergeDiscovery: %v", err)
	}
	want := DiscoveryDelta{Roots: []string{"/r/one"}, Policy: "strict"}
	if !reflect.DeepEqual(delta, want) || len(store.have.Nodes) != 0 {
		t.Fatalf("delta = %+v nodes = %v, want %+v and no node", delta, store.have.Nodes, want)
	}
}

// errLAN always returns an error from Scan.
type errLAN struct{ err error }

func (e *errLAN) Scan(context.Context) ([]string, error) { return nil, e.err }

// errPairer returns errors from Paired or Pair.
type errPairer struct {
	pairErr   error
	pairedErr error
}

func (p *errPairer) Paired(context.Context) ([]string, error)          { return nil, p.pairedErr }
func (p *errPairer) Pair(_ context.Context, _, _ string, _ bool) error { return p.pairErr }

// errDiscoveryStore returns an error from Discovery.
type errDiscoveryStore struct{ err error }

func (s *errDiscoveryStore) Discovery(context.Context) (DiscoveryState, error) {
	return DiscoveryState{}, s.err
}
func (s *errDiscoveryStore) AddRoot(context.Context, string) error     { return nil }
func (s *errDiscoveryStore) AddNode(context.Context, string) error     { return nil }
func (s *errDiscoveryStore) AddProvider(context.Context, string) error { return nil }
func (s *errDiscoveryStore) SetPolicy(context.Context, string) error   { return nil }

// addErrDiscoveryStore fails on AddRoot.
type addErrDiscoveryStore struct {
	fakeDiscoveryStore
	addErr error
}

func (s *addErrDiscoveryStore) AddRoot(context.Context, string) error { return s.addErr }

// TestCoverageGapInit covers the remaining branches to reach the coverage ratchet.
func TestCoverageGapInit(t *testing.T) {
	t.Run("nodes step with empty config", func(t *testing.T) {
		isolateHome(t)
		w, _, _, _ := fixture(t, Options{Mode: ModeYes}, &scriptedPrompter{})
		res, err := w.StepNodes(context.Background(), initconfig.NodesConfig{}, &fakeLAN{}, &fakePairer{})
		if err != nil {
			t.Fatalf("StepNodes empty: %v", err)
		}
		if !reflect.DeepEqual(res, NodesResult{}) {
			t.Fatalf("result = %+v, want empty", res)
		}
	})

	t.Run("lan scan error", func(t *testing.T) {
		isolateHome(t)
		w, _, _, _ := fixture(t, Options{Mode: ModeYes}, &scriptedPrompter{})
		scanErr := fmt.Errorf("network down")
		_, err := w.StepNodes(context.Background(), initconfig.NodesConfig{ScanLAN: true}, &errLAN{err: scanErr}, &fakePairer{})
		if !errors.Is(err, scanErr) {
			t.Fatalf("err = %v, want %v", err, scanErr)
		}
	})

	t.Run("pairer list error", func(t *testing.T) {
		isolateHome(t)
		w, rec, _, _ := fixture(t, Options{Mode: ModeYes}, &scriptedPrompter{})
		rec.env[initconfig.NodeCodeEnv("a.invalid")] = "code-a"
		listErr := fmt.Errorf("list failed")
		_, err := w.StepNodes(context.Background(), initconfig.NodesConfig{Pair: []string{"a.invalid"}}, &fakeLAN{}, &errPairer{pairedErr: listErr})
		if !errors.Is(err, listErr) {
			t.Fatalf("err = %v, want %v", err, listErr)
		}
	})

}

// TestCoverageGapInitMore holds the second half of the init branch tests.
func TestCoverageGapInitMore(t *testing.T) {
	t.Run("pairer pair error", func(t *testing.T) {
		isolateHome(t)
		w, rec, _, _ := fixture(t, Options{Mode: ModeYes}, &scriptedPrompter{})
		rec.env[initconfig.NodeCodeEnv("a.invalid")] = "code-a"
		pairErr := fmt.Errorf("pair failed")
		_, err := w.StepNodes(context.Background(), initconfig.NodesConfig{Pair: []string{"a.invalid"}}, &fakeLAN{}, &errPairer{pairErr: pairErr})
		if !errors.Is(err, pairErr) {
			t.Fatalf("err = %v, want %v", err, pairErr)
		}
	})

	t.Run("reconverge discovery error", func(t *testing.T) {
		isolateHome(t)
		discErr := fmt.Errorf("disc failed")
		_, err := ReconvergeDiscovery(context.Background(), &errDiscoveryStore{err: discErr}, DiscoveryState{})
		if !errors.Is(err, discErr) {
			t.Fatalf("err = %v, want %v", err, discErr)
		}
	})

	t.Run("reconverge add root error", func(t *testing.T) {
		isolateHome(t)
		addErr := fmt.Errorf("add root failed")
		store := &addErrDiscoveryStore{addErr: addErr}
		_, err := ReconvergeDiscovery(context.Background(), store, DiscoveryState{Roots: []string{"/new"}})
		if !errors.Is(err, addErr) {
			t.Fatalf("err = %v, want %v", err, addErr)
		}
	})
}
