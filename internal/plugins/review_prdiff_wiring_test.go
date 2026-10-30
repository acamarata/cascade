package plugins

// Purpose (this file): the PR-diff wiring refusals that need no launched
// process, so they also compile and run on windows. The launch-path tests
// are in review_prdiff_wiring_launch_test.go (the launch fixture is
// !windows).

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/cascade"
)

// countingFetcher returns a fetcher whose provider counts launches and
// refuses to start anything.
func countingFetcher(t *testing.T, registry *egress.Registry) (*prDiffFetcher, *int) {
	t.Helper()
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
	launches := 0
	return newPRDiffFetcher(func(context.Context, string) (*TrustedHandle, error) {
		launches++
		return nil, errors.New("test: must not launch")
	}, registry), &launches
}

func fetchTestPR(f *prDiffFetcher, number int) ([]byte, error) {
	return f.FetchPRDiff(context.Background(), "octocat", "Hello-World", number)
}

// TestPRDiffWiringRefusesWithoutEngineFactory: an unset factory is
// KindUnavailable wrapping ErrNoProcessFirewall itself, and nothing launches.
func TestPRDiffWiringRefusesWithoutEngineFactory(t *testing.T) {
	f, launches := countingFetcher(t, egress.DefaultRegistry())
	egress.SetProcessEngineFactory(nil)
	t.Cleanup(func() { egress.SetProcessEngineFactory(processFirewallDefault) })
	got, err := fetchTestPR(f, 1)
	found := false
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		found = found || cause == egress.ErrNoProcessFirewall
	}
	if got != nil || !found || *launches != 0 {
		t.Fatalf("diff %q err %v sentinel %v launches %d", got, err, found, *launches)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Fatalf("kind %v, want KindUnavailable", kind)
	}
}

// TestPRDiffWiringDisabledClassRefuses: a disabled github-pr-diff class
// refuses before any launch.
func TestPRDiffWiringDisabledClassRefuses(t *testing.T) {
	registry := egress.NewRegistry()
	registry.MustRegister(egress.EgressClassGitHubPRDiff, egress.InterceptConfig{Enabled: false, Owner: "test"})
	f, launches := countingFetcher(t, registry)
	if _, err := fetchTestPR(f, 1); !errors.Is(err, egress.ErrClassDisabled) || *launches != 0 {
		t.Fatalf("err %v launches %d, want ErrClassDisabled and 0", err, *launches)
	}
	// Control: the same fetcher over the shipped registry gets past the
	// firewall and reaches the provider, so the refusal above is the class.
	f.registry = egress.DefaultRegistry()
	if _, err := fetchTestPR(f, 1); err == nil || *launches != 1 {
		t.Fatalf("control: err %v launches %d, want the provider reached once", err, *launches)
	}
}

// TestPRDiffWiringRefusesBadReferenceAndAlteredParams: a bad number never
// reaches the firewall; a firewall that changes the reference is refused
// before launch.
func TestPRDiffWiringRefusesBadReferenceAndAlteredParams(t *testing.T) {
	f, launches := countingFetcher(t, egress.DefaultRegistry())
	intercepts := 0
	f.intercept = func(_ context.Context, _ *egress.Registry, b []byte) ([]byte, error) {
		intercepts++
		return []byte(strings.Replace(string(b), "octocat", "attacker", 1)), nil
	}
	if _, err := fetchTestPR(f, 0); !cascade.HasKind(err, cascade.KindInvalidInput) || intercepts != 0 {
		t.Fatalf("number 0: err %v intercepts %d", err, intercepts)
	}
	if _, err := fetchTestPR(f, 1); !cascade.HasKind(err, cascade.KindPolicyDenied) || intercepts != 1 || *launches != 0 {
		t.Fatalf("altered: err %v intercepts %d launches %d", err, intercepts, *launches)
	}
}

// TestPRDiffWiringLaunchErrorMapping: only a not-installed launch error is
// rewritten; a policy refusal passes through as the same error value.
func TestPRDiffWiringLaunchErrorMapping(t *testing.T) {
	f, _ := countingFetcher(t, egress.DefaultRegistry())
	notFound := cascade.New(cascade.KindNotFound, "plugins: cascade-github is not installed")
	denied := cascade.New(cascade.KindPolicyDenied, "plugins: cascade-github is not an enabled, trusted process plugin")
	for _, tc := range []struct {
		err  error
		kind cascade.Kind
		text string
	}{
		{notFound, cascade.KindUnavailable, "run `cascade plugin add cascade-github`"},
		{denied, cascade.KindPolicyDenied, denied.Error()},
		{nil, cascade.KindInternal, "internal: plugins: the HandleProvider returned no Handle and no error"},
	} {
		f.provider = func(context.Context, string) (*TrustedHandle, error) { return nil, tc.err }
		_, err := fetchTestPR(f, 1)
		kind, _ := cascade.KindOf(err)
		if kind != tc.kind || !strings.Contains(err.Error(), tc.text) || (tc.err != nil && !errors.Is(err, tc.err)) {
			t.Errorf("launch err %v: got %v (kind %v), want kind %v containing %q", tc.err, err, kind, tc.kind, tc.text)
		}
		if tc.err == denied && err != denied {
			t.Errorf("policy refusal was rewrapped: %v", err)
		}
		if tc.err == nil && (err == nil || err.Error() != tc.text) {
			t.Errorf("nil handle: err %v, want exactly %q", err, tc.text)
		}
	}
}

// TestPRDiffWiringDecodeRefuses: the host refuses a result whose byte count
// does not match, one over 8 MiB, and one with unknown fields.
func TestPRDiffWiringDecodeRefuses(t *testing.T) {
	big := strings.Repeat("x", 8<<20+1)
	for _, tc := range []struct {
		out  string
		kind cascade.Kind
	}{
		{`{"diff":"abc","bytes":2}`, cascade.KindIntegrity},
		{`{"diff":"abc","bytes":3,"extra":1}`, cascade.KindIntegrity},
		{`"abc"`, cascade.KindIntegrity},
		{`{"diff":"` + big + `","bytes":` + "8388609" + `}`, cascade.KindInvalidInput},
	} {
		got, err := decodePRDiffResult([]byte(tc.out))
		if kind, ok := cascade.KindOf(err); !ok || kind != tc.kind || got != nil {
			t.Errorf("%.40s: got %d bytes err %v, want %v", tc.out, len(got), err, tc.kind)
		}
	}
	got, err := decodePRDiffResult([]byte(`{"diff":"abc","bytes":3}`))
	if err != nil || string(got) != "abc" {
		t.Fatalf("valid result: %q %v", got, err)
	}
}

// TestInProcessPRDiffFetcherIsTheWiredPath (P1-PLG-14): the accessor hands
// out the same in-process fetch path init installs: the installed-plugin
// provider and the default egress registry, so a bad reference is refused
// before any launch exactly as before.
func TestInProcessPRDiffFetcherIsTheWiredPath(t *testing.T) {
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
	f, ok := InProcessPRDiffFetcher().(*prDiffFetcher)
	if !ok || f.provider == nil || f.registry != egress.DefaultRegistry() || f.intercept == nil {
		t.Fatalf("InProcessPRDiffFetcher = %#v, want the provider-backed fetcher over the default registry", f)
	}
	if _, err := f.FetchPRDiff(t.Context(), "octocat", "Hello-World", 0); kindOf(err) != cascade.KindInvalidInput {
		t.Fatalf("number 0: err %v, want invalid-input before any launch", err)
	}
}

func kindOf(err error) cascade.Kind {
	k, _ := cascade.KindOf(err)
	return k
}
