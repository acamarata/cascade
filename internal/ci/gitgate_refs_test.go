package ci

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

type gateFixture struct {
	root, a, b string
}

func gateRepo(t *testing.T) gateFixture {
	t.Helper()
	root := baselineRepo(t)
	a := baselineCommit(t, root)
	b := baselineCommit(t, root)
	return gateFixture{root: root, a: a, b: b}
}

func gateZeros() string { return strings.Repeat("0", 40) }

func neverVerify(t *testing.T) func(string) error {
	return func(commit string) error {
		t.Fatalf("verify called with %s on a refused push", commit)
		return nil
	}
}

func recordVerify(calls *[]string, result error) func(string) error {
	return func(commit string) error {
		*calls = append(*calls, commit)
		return result
	}
}

func wantRefusal(t *testing.T, err, sentinel error) {
	t.Helper()
	if !errors.Is(err, sentinel) || !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("want %v with KindPermissionDenied, got %v", sentinel, err)
	}
}

func TestGitGateForcePushRefused(t *testing.T) {
	fx := gateRepo(t)
	ctx := context.Background()
	err := CheckPush(ctx, fx.root, []PreReceiveRef{{Old: fx.b, New: fx.a, Ref: "refs/heads/main"}}, neverVerify(t))
	wantRefusal(t, err, ErrForcePushRefused)
	baselineGitTest(t, fx.root, "checkout", "-b", "side", fx.a)
	baselineGitTest(t, fx.root, "commit", "--allow-empty", "-m", "side")
	c := baselineGitTest(t, fx.root, "rev-parse", "HEAD")
	if c == fx.b {
		t.Fatal("side commit collided with B")
	}
	err = CheckPush(ctx, fx.root, []PreReceiveRef{{Old: fx.b, New: c, Ref: "refs/heads/main"}}, neverVerify(t))
	wantRefusal(t, err, ErrForcePushRefused)
}

func TestGitGateRefDeletionRefused(t *testing.T) {
	fx := gateRepo(t)
	err := CheckPush(context.Background(), fx.root, []PreReceiveRef{{Old: fx.b, New: gateZeros(), Ref: "refs/heads/main"}}, neverVerify(t))
	wantRefusal(t, err, ErrRefDeletionRefused)
}

func TestGitGateTagRequiresAttestation(t *testing.T) {
	fx := gateRepo(t)
	ctx := context.Background()
	baselineGitTest(t, fx.root, "tag", "-a", "v1", "-m", "release", fx.b)
	tagObj := baselineGitTest(t, fx.root, "rev-parse", "refs/tags/v1")
	if tagObj == fx.b {
		t.Fatal("annotated tag resolved to its commit")
	}
	denied := errors.New("not attested")
	var calls []string
	err := CheckPush(ctx, fx.root, []PreReceiveRef{{Old: gateZeros(), New: tagObj, Ref: "refs/tags/v1"}}, recordVerify(&calls, denied))
	if !errors.Is(err, denied) || len(calls) != 1 || calls[0] != fx.b {
		t.Fatalf("tag push: err %v calls %v want [%s]", err, calls, fx.b)
	}
	err = CheckPush(ctx, fx.root, []PreReceiveRef{{Old: tagObj, New: fx.a, Ref: "refs/tags/v1"}}, neverVerify(t))
	wantRefusal(t, err, ErrForcePushRefused)
}

func TestGitGateFastForwardVerifiesTip(t *testing.T) {
	fx := gateRepo(t)
	ctx := context.Background()
	var calls []string
	err := CheckPush(ctx, fx.root, []PreReceiveRef{{Old: fx.a, New: fx.b, Ref: "refs/heads/main"}}, recordVerify(&calls, nil))
	if err != nil || len(calls) != 1 || calls[0] != fx.b {
		t.Fatalf("fast-forward: err %v calls %v", err, calls)
	}
	calls = nil
	err = CheckPush(ctx, fx.root, []PreReceiveRef{{Old: gateZeros(), New: fx.a, Ref: "refs/heads/feature"}}, recordVerify(&calls, nil))
	if err != nil || len(calls) != 1 || calls[0] != fx.a {
		t.Fatalf("new branch at existing commit: err %v calls %v", err, calls)
	}
}

func TestGitGateCheckPushGuards(t *testing.T) {
	fx := gateRepo(t)
	ctx := context.Background()
	ok := []PreReceiveRef{{Old: fx.a, New: fx.b, Ref: "refs/heads/main"}}
	if err := CheckPush(ctx, fx.root, ok, nil); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("nil verify: %v", err)
	}
	for _, bad := range []PreReceiveRef{
		{Old: fx.a, New: fx.b, Ref: ""},
		{Old: fx.a, New: fx.b, Ref: "refs/heads/../x"},
		{Old: "oops", New: fx.b, Ref: "refs/heads/main"},
		{Old: strings.Repeat("0", 64), New: fx.b, Ref: "refs/heads/main"},
	} {
		if err := CheckPush(ctx, fx.root, []PreReceiveRef{bad}, neverVerify(t)); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("malformed %+v: %v", bad, err)
		}
	}
	missing := strings.Repeat("e", 40)
	err := CheckPush(ctx, fx.root, []PreReceiveRef{{Old: gateZeros(), New: missing, Ref: "refs/heads/x"}}, neverVerify(t))
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("missing object: %v", err)
	}
	tree := baselineGitTest(t, fx.root, "rev-parse", fx.b+"^{tree}")
	err = CheckPush(ctx, fx.root, []PreReceiveRef{{Old: gateZeros(), New: tree, Ref: "refs/heads/x"}}, neverVerify(t))
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("tree tip: %v", err)
	}
	var calls []string
	two := []PreReceiveRef{ok[0], {Old: fx.b, New: fx.a, Ref: "refs/heads/other"}}
	if err := CheckPush(ctx, fx.root, two, recordVerify(&calls, nil)); !errors.Is(err, ErrForcePushRefused) || len(calls) != 1 {
		t.Fatalf("second ref: err %v calls %v", err, calls)
	}
}

func TestParsePreReceiveStdin(t *testing.T) {
	old, nw := strings.Repeat("a", 40), strings.Repeat("b", 40)
	line := old + " " + nw + " refs/heads/main"
	got, err := ParsePreReceiveStdin(strings.NewReader(line + "\n"))
	if err != nil || len(got) != 1 || got[0] != (PreReceiveRef{Old: old, New: nw, Ref: "refs/heads/main"}) {
		t.Fatalf("valid line: %v %v", got, err)
	}
	got, err = ParsePreReceiveStdin(strings.NewReader(""))
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty input: %v %v", got, err)
	}
	if _, err := ParsePreReceiveStdin(nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("nil reader: %v", err)
	}
	bad := map[string]string{
		"oops":            "oops\n",
		"duplicate":       line + "\n" + line + "\n",
		"dotdot":          old + " " + nw + " refs/heads/../bad\n",
		"double space":    old + "  " + nw + " refs/heads/main\n",
		"trailing blank":  line + "\n\n",
		"mixed lengths":   old + " " + strings.Repeat("b", 64) + " refs/heads/main\n",
		"outside refs":    old + " " + nw + " HEAD\n",
		"blank ref":       old + " " + nw + " \n",
		"reflog syntax":   old + " " + nw + " refs/heads/a@{1}\n",
		"lock part":       old + " " + nw + " refs/heads/x.lock\n",
		"dot part":        old + " " + nw + " refs/heads/.hidden\n",
		"control char":    old + " " + nw + " refs/heads/a\tb\n",
		"glob char":       old + " " + nw + " refs/heads/a*\n",
		"upper hex":       strings.Repeat("A", 40) + " " + nw + " refs/heads/main\n",
		"carriage return": line + "\r\n",
		"long line":       old + " " + nw + " refs/heads/" + strings.Repeat("x", preReceiveMaxLine) + "\n",
	}
	for name, in := range bad {
		if _, err := ParsePreReceiveStdin(strings.NewReader(in)); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("%s: want KindInvalidInput, got %v", name, err)
		}
	}
}
