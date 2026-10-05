package generate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

const nested = "sub/AGENTS.md"

// swapDirForSymlink moves repo/sub aside and links it to outside.
func swapDirForSymlink(t *testing.T, repo, outside string) {
	t.Helper()
	if err := os.Rename(filepath.Join(repo, "sub"), filepath.Join(repo, "sub-old")); err != nil {
		t.Fatal(err)
	}
	symlink(t, outside, filepath.Join(repo, "sub"))
}

func TestLateEscapeCallsTheSink(t *testing.T) {
	ctx := context.Background()
	t.Run("Apply pre-publish walk", func(t *testing.T) {
		repo, home := newRepo(t)
		m := generated(t, repo, Rendered{Path: nested, GeneratorID: genA, Content: []byte(baseFile())})
		outside := standIn(t, home, "global")
		before := snapshot(t, outside)
		beforePublish = func(string) { swapDirForSymlink(t, repo, outside) }
		t.Cleanup(func() { beforePublish = func(string) {} })
		rec := &recorder{}
		w, _ := NewWriter(repo, rec.sink(), m, true)
		out, err := w.Apply(ctx, Rendered{Path: nested, GeneratorID: genA, Content: []byte(newFile())})
		escapes(t, out, err, rec, nested)
		sameTree(t, before, snapshot(t, outside))
	})
	t.Run("ApplyBlock pre-publish walk", func(t *testing.T) {
		repo, home := newRepo(t)
		m := generated(t, repo, Rendered{Path: nested, GeneratorID: genA, Content: []byte(baseFile())})
		outside := standIn(t, home, "global")
		before := snapshot(t, outside)
		beforePublish = func(string) { swapDirForSymlink(t, repo, outside) }
		t.Cleanup(func() { beforePublish = func(string) {} })
		rec := &recorder{}
		w, _ := NewWriter(repo, rec.sink(), m, true)
		out, err := w.ApplyBlock(ctx, nested, genA, "main", []byte("two\n"))
		escapes(t, out, err, rec, nested)
		sameTree(t, before, snapshot(t, outside))
	})
	t.Run("Apply re-read after a lost creation race", func(t *testing.T) {
		repo, home := newRepo(t)
		outside := standIn(t, home, "global")
		put(t, outside, "AGENTS.md", "owner file\n")
		before := snapshot(t, outside)
		beforeCreate = func(string) { swapDirForSymlink(t, repo, outside) }
		t.Cleanup(func() { beforeCreate = func(string) {} })
		rec := &recorder{}
		w, _ := NewWriter(repo, rec.sink(), Manifest{}, false)
		out, err := w.Apply(ctx, Rendered{Path: nested, GeneratorID: genA, Content: []byte(baseFile())})
		escapes(t, out, err, rec, nested)
		sameTree(t, before, snapshot(t, outside))
	})
}

func TestAdoptionRequiresTheRecordedGenerator(t *testing.T) {
	ctx := context.Background()
	t.Run("a file owned by another generator is refused, not adopted", func(t *testing.T) {
		repo, _ := newRepo(t)
		m := generated(t, repo, Rendered{Path: target, GeneratorID: genB, Content: []byte(baseFile())})
		rec := &recorder{}
		w, _ := NewWriter(repo, rec.sink(), m, true)
		out, err := w.Apply(ctx, Rendered{Path: target, GeneratorID: genA, Content: []byte(baseFile())})
		if err != nil || out.Adopted || out.Written || out.Reason != ReasonUnmanagedFile || len(rec.calls) != 1 {
			t.Fatalf("outcome = %+v %v", out, err)
		}
		if e := w.Manifest(fixedNow, nil).Entries[0]; e.GeneratorID != genB || e.Blocks[0].GeneratorID != genB {
			t.Fatalf("ownership moved to the refused generator: %+v", e)
		}
	})
	t.Run("the owner adopts and keeps foreign block owners", func(t *testing.T) {
		repo, m := twoGenerators(t)
		rec := &recorder{}
		w, _ := NewWriter(repo, rec.sink(), m, true)
		out, err := w.Apply(ctx, Rendered{Path: target, GeneratorID: genA, Content: []byte(slurp(t, repo, target))})
		if err != nil || !out.Adopted || out.Written || len(rec.calls) != 0 {
			t.Fatalf("outcome = %+v %v", out, err)
		}
		e := w.Manifest(fixedNow, nil).Entries[0]
		if e.GeneratorID != genA || len(e.Blocks) != 2 || e.Blocks[0].ID != "extra" || e.Blocks[0].GeneratorID != genB ||
			e.Blocks[1].GeneratorID != genA {
			t.Fatalf("owners not kept: %+v", e)
		}
	})
}
