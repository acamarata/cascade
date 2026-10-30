package delegation

// Purpose: MintRoot/MintChild tests and the package's shared fixtures: an
//
//	in-memory envelope.Signer seeded under t.TempDir (the pinned key comes
//	from the same seam) and named Sources.
//
// SPORT: delegation/mint/ADD (P1-AGT-11).

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
	"github.com/acamarata/cascade/pkg/provider"
)

var t0 = time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)

type testSigner struct{ priv ed25519.PrivateKey }

func (s testSigner) PublicKey() ed25519.PublicKey { return s.priv.Public().(ed25519.PublicKey) }

func (s testSigner) Sign(_ context.Context, msg []byte) ([]byte, error) {
	return ed25519.Sign(s.priv, msg), nil
}

// newSigner returns a signer whose seed lives under t.TempDir, and sets a
// fresh HOME so nothing reaches the operator's keychain or home.
func newSigner(t *testing.T) testSigner {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(t.TempDir(), "controller.seed"), seed, 0o600); err != nil {
		t.Fatal(err)
	}
	return testSigner{priv: ed25519.NewKeyFromSeed(seed)}
}

// srcMut is a mutation delegation's Sources.
func srcMut() Sources {
	return Sources{
		RepoRoot: "/repo", InScope: []string{"pkg/x/", "internal/jobs", "internal/jobs/."},
		ToolPolicy: "all", Sensitivity: provider.SensitivityInternal,
		Budget: envelope.Budget{WallClock: time.Hour, Tokens: 1000, CostMicros: 50, Procs: 4},
		Now:    t0, TTL: time.Hour,
	}
}

// wantErr asserts err's Kind and that its message names want.
func wantErr(t *testing.T, err error, kind cascade.Kind, want string) {
	t.Helper()
	if err == nil || !cascade.HasKind(err, kind) || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want Kind %s naming %q", err, kind, want)
	}
}

func TestMintRootFromNamedSources(t *testing.T) {
	s := newSigner(t)
	ctx := context.Background()
	src := srcMut()
	signed, err := MintRoot(ctx, s, principal.Principal{ID: "p-owner", ExpiresAt: t0.Add(2 * time.Hour)}, src)
	if err != nil {
		t.Fatal(err)
	}
	env, err := envelope.Verify(signed, s.PublicKey(), t0)
	if err != nil {
		t.Fatal(err)
	}
	want := envelope.Envelope{
		Version: envelope.Version, ID: env.ID, Issuer: env.Issuer, Principal: "p-owner",
		Roots: []string{"/repo/internal/jobs", "/repo/pkg/x"}, Tools: []string{"agent.tools.all"},
		Commands: []string{}, Egress: []string{"agent-driver"}, Grants: []envelope.GrantRef{},
		Budget: src.Budget, Sensitivity: provider.SensitivityInternal, IssuedAt: t0, ExpiresAt: t0.Add(time.Hour),
	}
	if !reflect.DeepEqual(env, want) || !strings.HasPrefix(env.ID, "dlg_") || len(env.ID) != 36 {
		t.Fatalf("root envelope:\n got %+v\nwant %+v", env, want)
	}
	ro := Sources{ReadOnlyDir: "/var/ro", ToolPolicy: "readonly", Sensitivity: provider.SensitivityLocalOnly, Now: t0, TTL: time.Hour}
	signed, err = MintRoot(ctx, s, principal.Principal{ID: OwnerCLI, ExpiresAt: t0.Add(30 * time.Minute)}, ro)
	if err != nil {
		t.Fatal(err)
	}
	e := signed.Env
	if !reflect.DeepEqual(e.Roots, []string{"/var/ro"}) || e.Tools[0] != "agent.tools.readonly" || e.Principal != OwnerCLI ||
		!e.ExpiresAt.Equal(t0.Add(30*time.Minute)) || e.Sensitivity != provider.SensitivityLocalOnly {
		t.Fatalf("read-only root = %+v", e)
	}
	var fields []string
	for i, ty := 0, reflect.TypeOf(Sources{}); i < ty.NumField(); i++ {
		fields = append(fields, ty.Field(i).Name)
	}
	if strings.Join(fields, ",") != "RepoRoot,InScope,ReadOnlyDir,ToolPolicy,Sensitivity,Budget,Now,TTL" {
		t.Fatalf("Sources fields = %v: no caller root, tool or egress field may exist", fields)
	}
}

func TestMintRootRefusals(t *testing.T) {
	s := newSigner(t)
	owner := principal.Principal{ID: "p-owner"}
	cases := []struct {
		mut  func(*Sources)
		want string
	}{
		{func(x *Sources) { x.Now = time.Time{} }, "Now and a positive TTL"},
		{func(x *Sources) { x.TTL = 0 }, "Now and a positive TTL"},
		{func(x *Sources) { x.ToolPolicy = "root" }, "ToolPolicy"},
		{func(x *Sources) { x.Sensitivity = 99 }, "not a declared tier"},
		{func(x *Sources) { x.ReadOnlyDir = "/ro" }, "RepoRoot with InScope"},
		{func(x *Sources) { x.InScope = nil }, "RepoRoot with InScope"},
		{func(x *Sources) { x.RepoRoot = "repo" }, "RepoRoot \"repo\""},
		{func(x *Sources) { x.RepoRoot = "/repo/../etc" }, "not an absolute clean path"},
		{func(x *Sources) { x.InScope = []string{"a/../../etc"} }, "not a relative path"},
		{func(x *Sources) { x.InScope = []string{"/etc"} }, "not a relative path"},
		{func(x *Sources) { x.InScope = []string{"a/**"} }, "not a relative path"},
		{func(x *Sources) { x.InScope = []string{`a\b`} }, "not a relative path"},
		{func(x *Sources) { x.InScope = []string{""} }, "not a relative path"},
		{func(x *Sources) { *x = Sources{ReadOnlyDir: "ro", ToolPolicy: "none", Now: t0, TTL: 1} }, "ReadOnlyDir"},
	}
	for _, c := range cases {
		src := srcMut()
		c.mut(&src)
		_, err := MintRoot(context.Background(), s, owner, src)
		wantErr(t, err, cascade.KindInvalidInput, c.want)
	}
	_, err := MintRoot(context.Background(), s, principal.Principal{}, srcMut())
	wantErr(t, err, cascade.KindInvalidInput, "owner principal ID is required")
	_, err = MintRoot(context.Background(), s, principal.Principal{ID: "p", ExpiresAt: t0}, srcMut())
	wantErr(t, err, cascade.KindPermissionDenied, "owner principal has expired")
}

// extParent issues a root carrying a registered extension and a fence.
func extParent(t *testing.T, s testSigner) envelope.Signed {
	t.Helper()
	root, err := MintRoot(context.Background(), s, principal.Principal{ID: OwnerCLI}, srcMut())
	if err != nil {
		t.Fatal(err)
	}
	e := root.Env
	e.Ext = []envelope.Extension{{Kind: testExtKind, Data: json.RawMessage(`{"n":2}`)}}
	e.Fence = envelope.Fence{DispatchID: "d1", Attempt: 3, LeaseEpoch: 9}
	p, err := envelope.Issue(context.Background(), s, e)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

const testExtKind = "delegation-test.budget"

func init() {
	envelope.RegisterExtension(testExtKind, func(p, c json.RawMessage) error {
		if string(p) != string(c) {
			return errors.New("extension data differs")
		}
		return nil
	})
}

func TestMintChildNarrowsParent(t *testing.T) {
	s := newSigner(t)
	ctx := context.Background()
	parent := extParent(t, s)
	src := srcMut()
	src.InScope, src.Budget.Tokens, src.TTL, src.Sensitivity = []string{"internal/jobs/sub"}, 10, time.Minute, provider.SensitivityPublic
	child, err := MintChild(ctx, s, parent, s.PublicKey(), "p-child", src)
	if err != nil {
		t.Fatal(err)
	}
	c := child.Env
	pd, _ := envelope.Digest(parent.Env)
	if c.Fence != parent.Env.Fence || !reflect.DeepEqual(c.Ext, parent.Env.Ext) || c.Issuer != parent.Env.Issuer ||
		c.ParentDigest != pd || c.Principal != "p-child" || c.Sensitivity != provider.SensitivityInternal ||
		!reflect.DeepEqual(c.Roots, []string{"/repo/internal/jobs/sub"}) || len(c.Ext) != 1 {
		t.Fatalf("child = %+v", c)
	}
	wider := map[string]func(*Sources){
		"roots":  func(x *Sources) { x.InScope = []string{"cmd"} },
		"budget": func(x *Sources) { x.Budget.Procs = 5 },
		"exp":    func(x *Sources) { x.TTL = 2 * time.Hour },
		"tools":  func(x *Sources) { x.ToolPolicy = "none" },
	}
	for field, mut := range wider {
		w := srcMut()
		mut(&w)
		_, err := MintChild(ctx, s, parent, s.PublicKey(), "p-child", w)
		var ne *envelope.NarrowError
		if !errors.As(err, &ne) || ne.Field != field || !cascade.HasKind(err, cascade.KindPermissionDenied) {
			t.Fatalf("wider %s: err = %v, want *envelope.NarrowError on %s", field, err, field)
		}
	}
	other := newSigner(t)
	_, err = MintChild(ctx, s, parent, other.PublicKey(), "p-child", srcMut())
	wantErr(t, err, cascade.KindIntegrity, "delegation: parent")
	late := srcMut()
	late.Now = t0.Add(time.Hour)
	_, err = MintChild(ctx, s, parent, s.PublicKey(), "p-child", late)
	wantErr(t, err, cascade.KindPermissionDenied, "expired")
	for _, p := range []string{"", OwnerCLI} {
		_, err = MintChild(ctx, s, parent, s.PublicKey(), p, srcMut())
		wantErr(t, err, cascade.KindInvalidInput, "child principal ID")
	}
	_, err = MintChild(ctx, s, parent, s.PublicKey(), "p-child", Sources{})
	wantErr(t, err, cascade.KindInvalidInput, "Now and a positive TTL")
	if caps := [4]int{MaxDepth, MaxLiveChildren, jobs.DelegationMaxDepth, jobs.DelegationMaxLiveChildren}; caps != [4]int{3, 8, 3, 8} {
		t.Fatalf("caps = %v, want MaxDepth 3 and MaxLiveChildren 8 aliasing jobs", caps)
	}
}
