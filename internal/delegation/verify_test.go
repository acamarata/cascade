package delegation

// Purpose: VerifyChain tests against a real jobs store (fixture in
//
//	gate_test.go). Every refusal test first proves a chain verifies
//	untampered, so a VerifyChain that refuses everything fails.
//
// SPORT: delegation/verify/ADD (P1-AGT-11).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
)

func TestVerifyChainRefusesUnsigned(t *testing.T) {
	c := newChain(t)
	child, rec := c.add(c.root)
	c.mustVerify(rec)
	unsigned := c.wire(envelope.Signed{Env: child.Env, Sig: []byte{}})
	forged := rec
	forged.Signed = unsigned
	_, err := c.verify(forged, t0)
	wantErr(t, err, cascade.KindIntegrity, "differs from its stored row")
	e := child.Env
	e.ID, e.Principal = "unsigned", "agent-unsigned"
	_, err = c.insert(c.wire(envelope.Signed{Env: e, Sig: []byte{}}), e, c.rootB)
	wantErr(t, err, cascade.KindInvalidInput, "signature has length 0")
	sib, err := MintChild(context.Background(), c.s, c.root, c.s.PublicKey(), "agent-sib", srcMut())
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.insert(c.wire(sib), sib.Env, c.wire(envelope.Signed{Env: c.root.Env, Sig: []byte{}}))
	wantErr(t, err, cascade.KindIntegrity, "ParentSigned does not decode")
	_, err = VerifyChain(context.Background(), nil, rec, c.s.PublicKey(), t0)
	wantErr(t, err, cascade.KindInvalidInput, "jobs store is required")
	_, err = c.verify(jobs.DelegationRecord{ID: "nope"}, t0)
	wantErr(t, err, cascade.KindNotFound, "delegation: record")
}

func TestVerifyChainRefusesTampered(t *testing.T) {
	c := newChain(t)
	_, rec := c.add(c.root)
	c.mustVerify(rec)
	r2, err := MintRoot(context.Background(), c.s, principal.Principal{ID: "p-2"}, srcMut())
	if err != nil {
		t.Fatal(err)
	}
	bad, err := c.insert(tamper(t, c.wire(r2)), r2.Env, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.verify(bad, t0)
	wantErr(t, err, cascade.KindIntegrity, "signature does not verify")
	sib, err := MintChild(context.Background(), c.s, c.root, c.s.PublicKey(), "agent-sib", srcMut())
	if err != nil {
		t.Fatal(err)
	}
	sibRec, err := c.insert(c.wire(sib), sib.Env, tamper(t, c.rootB))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.verify(sibRec, t0)
	wantErr(t, err, cascade.KindIntegrity, "verify parent of "+sib.Env.ID)
	forged := rec
	forged.Signed = append([]byte(nil), rec.Signed...)
	forged.Signed[len(forged.Signed)-3] ^= 1
	_, err = c.verify(forged, t0)
	wantErr(t, err, cascade.KindIntegrity, "differs from its stored row")
}

func TestVerifyChainRefusesExpired(t *testing.T) {
	c := newChain(t)
	_, rec := c.add(c.root)
	c.mustVerify(rec)
	_, err := c.verify(rec, t0.Add(time.Hour))
	wantErr(t, err, cascade.KindPermissionDenied, "expired")
	_, err = c.verify(rec, t0.Add(-time.Second))
	wantErr(t, err, cascade.KindPermissionDenied, "not yet valid")
	if err := c.st.FinishDelegation(context.Background(), rec.ID, jobs.DelegationSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	rec.State = jobs.DelegationSucceeded
	_, err = c.verify(rec, t0)
	wantErr(t, err, cascade.KindPermissionDenied, "state: record")
}

func TestVerifyChainRefusesWider(t *testing.T) {
	c := newChain(t)
	p, _ := c.add(c.root)
	_, sibling := c.add(p)
	c.mustVerify(sibling)
	pd, _ := envelope.Digest(p.Env)
	e := p.Env
	e.ID, e.Principal, e.ParentDigest, e.Roots = "wide", "agent-wide", pd, []string{"/elsewhere"}
	wide, err := envelope.Issue(context.Background(), c.s, e)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := c.insert(c.wire(wide), wide.Env, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.verify(rec, t0)
	var ne *envelope.NarrowError
	wantErr(t, err, cascade.KindPermissionDenied, "delegation: narrow wide")
	if !errors.As(err, &ne) || ne.Field != "roots" {
		t.Fatalf("err = %v, want *envelope.NarrowError on roots", err)
	}
}

func TestDepthClampRefuses(t *testing.T) {
	c := newChain(t)
	d1, _ := c.add(c.root)
	d2, _ := c.add(d1)
	d3, r3 := c.add(d2)
	if r3.Depth != MaxDepth {
		t.Fatalf("depth = %d, want %d", r3.Depth, MaxDepth)
	}
	c.mustVerify(r3)
	d4, err := MintChild(context.Background(), c.s, d3, c.s.PublicKey(), "agent-4", srcMut())
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.insert(c.wire(d4), d4.Env, nil)
	wantErr(t, err, cascade.KindPermissionDenied, "depth 4 exceeds MaxDepth 3")
}

func TestCycleRefused(t *testing.T) {
	c := newChain(t)
	a, ra := c.add(c.root)
	c.mustVerify(ra)
	b, err := MintChild(context.Background(), c.s, a, c.s.PublicKey(), "agent-b", srcMut())
	if err != nil {
		t.Fatal(err)
	}
	e := b.Env
	e.ID = c.root.Env.ID
	b, err = envelope.Issue(context.Background(), c.s, e)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := c.insert(c.wire(b), b.Env, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.verify(rb, t0)
	wantErr(t, err, cascade.KindIntegrity, fmt.Sprintf("cycle: an ancestor of %q", a.Env.ID))
}

func TestFanOutRefused(t *testing.T) {
	c := newChain(t)
	p, rp := c.add(c.root)
	for i := 0; i < MaxLiveChildren-1; i++ {
		c.add(p)
	}
	c.mustVerify(rp)
	c.add(p)
	_, err := c.verify(rp, t0)
	wantErr(t, err, cascade.KindPermissionDenied, fmt.Sprintf("fan-out: record %q holds 8 live children", rp.ID))
}

func TestVerifyChainWalksToRoot(t *testing.T) {
	c := newChain(t)
	g, err := MintChild(context.Background(), c.s, c.root, c.s.PublicKey(), "agent-g", srcMut())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.insert(c.wire(g), g.Env, tamper(t, c.rootB)); err != nil {
		t.Fatal(err)
	}
	p, rp := c.add(g)
	_, rc := c.add(p)
	_, okRec := c.add(c.root)
	c.mustVerify(okRec)
	if _, err := envelope.Verify(p, c.s.PublicKey(), t0); err != nil || rc.Depth != 3 || rp.Depth != 2 {
		t.Fatalf("the grandchild's own parent must verify: %v (depths %d/%d)", err, rp.Depth, rc.Depth)
	}
	_, err = c.verify(rc, t0)
	wantErr(t, err, cascade.KindIntegrity, "verify parent of "+g.Env.ID)
	stored, err := c.insert(c.wire(c.root), c.root.Env, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.mustVerify(stored)
}

func TestVerifyChainRefusesForgedRows(t *testing.T) {
	c := newChain(t)
	ctx := context.Background()
	mint := func(p envelope.Signed, who string) (envelope.Signed, string) {
		s, err := MintChild(ctx, c.s, p, c.s.PublicKey(), who, srcMut())
		if err != nil {
			t.Fatal(err)
		}
		d, _ := envelope.Digest(s.Env)
		return s, d
	}
	a, ra := c.add(c.root)
	c.mustVerify(ra)
	x, xd := mint(c.root, "agent-x")
	x2, _ := mint(c.root, "agent-x2")
	y, yd := mint(x2, "agent-y")
	z, zd := mint(a, "agent-z")
	v, vd := mint(c.root, "agent-v")
	u, ud := mint(c.root, "agent-u")
	w, wd := mint(a, "agent-w")
	other, _ := MintRoot(ctx, c.s, principal.Principal{ID: "p-other"}, srcMut())
	od, _ := envelope.Digest(other.Env)
	pretty, _ := json.MarshalIndent(a, "", "  ")
	cases := []struct {
		rec  jobs.DelegationRecord
		kind cascade.Kind
		want string
	}{
		{c.rawRow(x.Env.ID, "lie", c.wire(x), x.Env.ParentDigest, c.rootB, 1), cascade.KindIntegrity, "does not hash to its EnvelopeDigest"},
		{c.rawRow("other-id", xd, c.wire(x), x.Env.ParentDigest, c.rootB, 1), cascade.KindIntegrity, "names a different ID or parent"},
		{c.rawRow("deep", "d3", c.wire(x), x.Env.ParentDigest, c.rootB, 4), cascade.KindPermissionDenied, "has depth 4, MaxDepth is 3"},
		{c.rawRow("junk", "d4", []byte("{}"), "", nil, 0), cascade.KindIntegrity, "delegation: verify junk"},
		{c.rawRow(other.Env.ID, od, c.wire(other), "", nil, 2), cascade.KindIntegrity, "chain: root record"},
		{c.rawRow(y.Env.ID, yd, c.wire(y), y.Env.ParentDigest, c.wire(x2), 2), cascade.KindIntegrity, "ancestor record of"},
		{c.rawRow(z.Env.ID, zd, c.wire(z), z.Env.ParentDigest, c.wire(a), 3), cascade.KindIntegrity, "depth: stored depth 3"},
		{c.rawRow(w.Env.ID, wd, c.wire(w), w.Env.ParentDigest, pretty, 2), cascade.KindIntegrity, "is not its ParentSigned"},
		{c.rawRow(u.Env.ID, ud, c.wire(u), "", nil, 0), cascade.KindIntegrity, "names a different ID or parent"},
		{c.rawRow(v.Env.ID, vd, c.wire(v), v.Env.ParentDigest, c.wire(other), 1), cascade.KindIntegrity, "does not hash to its ParentDigest"},
	}
	for i, tc := range cases {
		_, err := c.verify(tc.rec, t0)
		if err == nil || !cascade.HasKind(err, tc.kind) || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("case %d: err = %v, want Kind %s naming %q", i, err, tc.kind, tc.want)
		}
	}
}

func TestVerifyChainRefusesDroppedExtension(t *testing.T) {
	c := newChain(t)
	ctx := context.Background()
	root := extParent(t, c.s)
	rootB := c.wire(root)
	a, err := MintChild(ctx, c.s, root, c.s.PublicKey(), "agent-a", srcMut())
	if err != nil || len(a.Env.Ext) != 1 {
		t.Fatalf("MintChild = %d ext, %v", len(a.Env.Ext), err)
	}
	ra, err := c.insert(c.wire(a), a.Env, rootB)
	if err != nil {
		t.Fatal(err)
	}
	c.mustVerify(ra)
	_, kept := c.add(a)
	c.mustVerify(kept)
	b, _ := MintChild(ctx, c.s, a, c.s.PublicKey(), "agent-b", srcMut())
	e := b.Env
	e.Ext = nil
	dropped, err := envelope.Issue(ctx, c.s, e)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := c.insert(c.wire(dropped), dropped.Env, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.verify(rb, t0)
	var ne *envelope.NarrowError
	if !errors.As(err, &ne) || ne.Field != "ext" || !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("err = %v, want *envelope.NarrowError on ext", err)
	}
}
