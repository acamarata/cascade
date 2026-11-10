package init

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/providers/intake"
	"github.com/acamarata/cascade/pkg/cascade"
)

// intakeSub stands in for the `cascade provider add` child process: it
// records the argv and upserts the record the real intake would mint,
// whose AuthRef is a vault key NAME. ref overrides that name, so a test
// can feed the step a record whose reference is not one.
type intakeSub struct {
	reg   *intake.MemoryRegistry
	calls [][]string
	ref   string
}

func (s *intakeSub) Run(ctx context.Context, args ...string) error {
	s.calls = append(s.calls, args)
	if len(args) < 4 || args[0] != "provider" || args[1] != "add" {
		return cascade.New(cascade.KindInvalidInput, "unexpected subprocess")
	}
	rec := intake.ProviderRecord{Name: args[2], Driver: intake.DriverAnthropic}
	switch args[3] {
	case "--oauth":
		rec.Auth, rec.AuthRef = intake.AuthOAuth, intake.VaultKeyRef("provider."+args[2]+".oauth.access")
	default:
		rec.Auth, rec.AuthRef = intake.AuthKey, intake.VaultKeyRef("provider."+args[2]+".key")
	}
	if len(args) == 6 && args[4] == "--pool" {
		rec.Pool = args[5]
	}
	if s.ref != "" {
		rec.AuthRef = intake.VaultKeyRef(s.ref)
	}
	return s.reg.UpsertProvider(ctx, rec)
}

// valueShaped is a reference that is not a real credential, but is not a
// name the intake would ever mint either.
const valueShaped = "literal-credential-value-0000"

// capacityCase is one row of TestRunCapacityStep. prompt is a value so
// each run drives its own copy of the scripted answers.
type capacityCase struct {
	name       string
	mode       Mode
	installed  []intake.ProviderRecord
	prompt     scriptedPrompter
	ref        string
	wantErr    cascade.Kind
	wantErrMsg string
	wantCalls  []string
	wantOut    []string
	notOut     []string
	wantNames  []string
	wantAsked  int
}

var capacityCases = []capacityCase{{
	name:      "nothing detected, one oauth account added",
	mode:      ModeInteractive,
	prompt:    scriptedPrompter{confirms: []bool{true, false}, lines: []string{"work", ""}, choices: []string{CredentialOAuth}},
	wantCalls: []string{"provider add work --oauth"},
	wantOut:   []string{"== capacity", "detected: none", "added: work (anthropic, oauth, ref provider.work.oauth.access)"},
	wantNames: []string{"work"},
	wantAsked: 5,
}, {
	name: "two detected, no additional accounts",
	mode: ModeInteractive,
	installed: []intake.ProviderRecord{
		{Name: "alpha", Driver: intake.DriverAnthropic, Auth: intake.AuthKey, AuthRef: "provider.alpha.key"},
		{Name: "beta", Driver: intake.DriverGemini, Auth: intake.AuthGateway, AuthRef: "gateway:team"},
	},
	prompt:    scriptedPrompter{confirms: []bool{false}},
	wantOut:   []string{"detected: alpha (anthropic, key, ref provider.alpha.key)", "detected: beta (gemini, gateway, ref gateway:team)"},
	notOut:    []string{"detected: none"},
	wantNames: []string{"alpha", "beta"},
	wantAsked: 1,
}, {
	name: "additional key account joins a pool",
	mode: ModeInteractive,
	installed: []intake.ProviderRecord{
		{Name: "alpha", Driver: intake.DriverAnthropic, Auth: intake.AuthKey, AuthRef: "provider.alpha.key", Pool: "team"},
	},
	prompt:    scriptedPrompter{confirms: []bool{true, false}, lines: []string{"alpha-2", "team"}, choices: []string{CredentialKeyEnv}},
	wantCalls: []string{"provider add alpha-2 --key-env --pool team"},
	wantOut:   []string{"detected: alpha (anthropic, key, ref provider.alpha.key, pool team)", "added: alpha-2 (anthropic, key, ref provider.alpha-2.key, pool team)"},
	wantNames: []string{"alpha", "alpha-2"},
	wantAsked: 5,
}, {
	name:      "check mode plans the add and runs nothing",
	mode:      ModeCheck,
	prompt:    scriptedPrompter{confirms: []bool{true, false}, lines: []string{"work", ""}, choices: []string{CredentialOAuth}},
	wantOut:   []string{"would add account work through `cascade provider add`"},
	wantNames: []string{"work"},
	wantAsked: 5,
}, {
	name:       "a reference that is not a vault ref is refused unprinted",
	mode:       ModeInteractive,
	prompt:     scriptedPrompter{confirms: []bool{true}, lines: []string{"work", ""}, choices: []string{CredentialKey}},
	ref:        valueShaped,
	wantErr:    cascade.KindIntegrity,
	wantErrMsg: `cascade init: provider "work" carries a credential reference that is not a vault reference; it was not printed`,
	wantCalls:  []string{"provider add work --key"},
	notOut:     []string{valueShaped, "added:"},
	wantAsked:  4,
}, {
	name: "a detected record with a value-shaped reference is refused",
	mode: ModeInteractive,
	installed: []intake.ProviderRecord{
		{Name: "alpha", Driver: intake.DriverAnthropic, Auth: intake.AuthKey, AuthRef: valueShaped},
	},
	prompt:     scriptedPrompter{},
	wantErr:    cascade.KindIntegrity,
	wantErrMsg: `cascade init: provider "alpha" carries a credential reference that is not a vault reference; it was not printed`,
	notOut:     []string{valueShaped},
}, {
	name:       "an empty account name is refused",
	mode:       ModeInteractive,
	prompt:     scriptedPrompter{confirms: []bool{true}, lines: []string{"  "}},
	wantErr:    cascade.KindInvalidInput,
	wantErrMsg: "cascade init: an additional account needs a name",
	wantAsked:  2,
}}

func TestRunCapacityStep(t *testing.T) {
	for _, tc := range capacityCases {
		t.Run(tc.name, func(t *testing.T) {
			prompt := tc.prompt
			runCapacityCase(t, tc.mode, tc.installed, &prompt, tc.ref, func(err error, out string, sub *intakeSub, state State) {
				assertCapacityErr(t, err, tc.wantErr, tc.wantErrMsg)
				for _, want := range tc.wantOut {
					outContains(t, out, want, "capacity output")
				}
				for _, bad := range tc.notOut {
					if strings.Contains(out, bad) {
						t.Fatalf("output carries %q:\n%s", bad, out)
					}
				}
				assertCalls(t, sub, tc.wantCalls)
				if got := len(prompt.asked); got != tc.wantAsked {
					t.Fatalf("prompts = %d %v, want %d", got, prompt.asked, tc.wantAsked)
				}
				if tc.wantErrMsg == "" && strings.Join(state.Providers, ",") != strings.Join(tc.wantNames, ",") {
					t.Fatalf("state.Providers = %v, want %v", state.Providers, tc.wantNames)
				}
			})
		})
	}
}

// runCapacityCase builds a wizard over a fresh HOME, seeds the registry,
// runs the step and hands the outcome to check.
func runCapacityCase(t *testing.T, mode Mode, installed []intake.ProviderRecord, prompt *scriptedPrompter,
	ref string, check func(error, string, *intakeSub, State)) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	ctx := context.Background()
	w, _, out, _ := fixture(t, Options{Mode: mode}, prompt)
	reg := intake.NewMemoryRegistry()
	names := make([]string, 0, len(installed))
	for _, rec := range installed {
		if err := reg.UpsertProvider(ctx, rec); err != nil {
			t.Fatalf("seed %s: %v", rec.Name, err)
		}
		names = append(names, rec.Name)
	}
	sub := &intakeSub{reg: reg, ref: ref}
	w.deps.Sub = sub
	w.deps.Current = fakeCurrent{providers: names}
	var state State
	err := w.StepCapacity(ctx, &state, reg)
	check(err, out.String(), sub, state)
}

// assertCapacityErr checks a refusal by kind AND message, or no error.
func assertCapacityErr(t *testing.T, err error, kind cascade.Kind, msg string) {
	t.Helper()
	if msg == "" {
		if err != nil {
			t.Fatalf("StepCapacity: %v", err)
		}
		return
	}
	assertTaxonomyErr(t, err, kind, msg)
}

// assertTaxonomyErr checks err is a *cascade.Error of kind carrying
// exactly msg: identity and message, never the kind alone.
func assertTaxonomyErr(t *testing.T, err error, kind cascade.Kind, msg string) {
	t.Helper()
	var ce *cascade.Error
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want a *cascade.Error", err)
	}
	if ce.Kind != kind || ce.Msg != msg {
		t.Fatalf("err = (%v, %q), want (%v, %q)", ce.Kind, ce.Msg, kind, msg)
	}
}

// outContains fails the test when out does not carry want.
func outContains(t *testing.T, out, want, what string) {
	t.Helper()
	if !strings.Contains(out, want) {
		t.Fatalf("%s does not carry %q:\n%s", what, want, out)
	}
}

// assertCalls checks the exact subprocess invocations, in order.
func assertCalls(t *testing.T, sub *intakeSub, want []string) {
	t.Helper()
	got := make([]string, 0, len(sub.calls))
	for _, c := range sub.calls {
		got = append(got, strings.Join(c, " "))
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("subprocess calls = %q, want %q", got, want)
	}
}
