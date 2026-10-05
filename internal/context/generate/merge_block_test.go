package generate

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

const genB = "gen-b"

// twoGenerators seeds target for genA, then lets genB add block "extra".
func twoGenerators(t *testing.T) (repo string, m Manifest) {
	t.Helper()
	repo, _ = newRepo(t)
	rec := &recorder{}
	w, err := NewWriter(repo, rec.sink(), seed(t, repo), true)
	if err != nil {
		t.Fatal(err)
	}
	out, err := w.ApplyBlock(context.Background(), target, genB, "extra", []byte("from b\n"))
	if err != nil || !out.Written || out.Reason != "" {
		t.Fatalf("ApplyBlock = %+v %v", out, err)
	}
	return repo, w.Manifest(fixedNow, nil)
}

func TestWriterApplyBlockForeignGenerator(t *testing.T) {
	repo, m := twoGenerators(t)
	got := slurp(t, repo, target)
	want := baseFile() + "<!-- cascade:managed:begin extra -->\nfrom b\n<!-- cascade:managed:end -->\n"
	if got != want {
		t.Fatalf("file = %q", got)
	}
	e := m.Entries[0]
	if e.GeneratorID != genA || len(e.Blocks) != 2 || e.Blocks[0].ID != "extra" || e.Blocks[0].GeneratorID != genB ||
		e.Blocks[1].ID != "main" || e.Blocks[1].GeneratorID != genA {
		t.Fatalf("entry = %+v", e)
	}
	if e.OutsideSHA256 != sum(baseFile()[:strings.Index(baseFile(), "<!--")]+"\nfooter\n") || e.FileSHA256 != sum(got) {
		t.Fatalf("hashes not recomputed from the new bytes: %+v", e)
	}
	if err := WriteManifest(repo, m); err != nil {
		t.Fatal(err)
	}
	if d, err := Check(context.Background(), repo); err != nil || !d.Clean() {
		t.Fatalf("a foreign block made Check red: %+v %v", d, err)
	}
	rec := &recorder{}
	out, err := applyNew(t, repo, m, true, rec)
	if err != nil || !out.Written || out.Reason != "" || len(rec.calls) != 0 {
		t.Fatalf("the owner's next generation refused after a foreign block: %+v %v", out, err)
	}
}

func TestWriterCarriesForeignBlocks(t *testing.T) {
	repo, m := twoGenerators(t)
	rec := &recorder{}
	w, _ := NewWriter(repo, rec.sink(), m, true)
	out, err := w.Apply(context.Background(), Rendered{Path: target, GeneratorID: genA, Content: []byte(newFile())})
	if err != nil || !out.Written {
		t.Fatalf("Apply = %+v %v", out, err)
	}
	want := newFile() + "<!-- cascade:managed:begin extra -->\nfrom b\n<!-- cascade:managed:end -->\n"
	if got := slurp(t, repo, target); got != want {
		t.Fatalf("foreign block not carried unchanged:\n%q", got)
	}
	next := w.Manifest(fixedNow, nil).Entries[0]
	if len(next.Blocks) != 2 || next.Blocks[0].GeneratorID != genB || next.Blocks[0].ContentSHA256 != sum("from b\n") ||
		next.Blocks[1].ContentSHA256 != sum("two\n") {
		t.Fatalf("entry = %+v", next)
	}
	t.Run("an edited foreign block refuses", func(t *testing.T) {
		put(t, repo, target, strings.Replace(want, "from b\n", "edited\n", 1))
		rec := &recorder{}
		w, _ := NewWriter(repo, rec.sink(), w.Manifest(fixedNow, nil), true)
		out, err := w.Apply(context.Background(), Rendered{Path: target, GeneratorID: genA, Content: []byte(baseFile())})
		if err != nil || out.Written || out.Reason != ReasonBlockEdited || len(rec.calls) != 1 {
			t.Fatalf("outcome = %+v %v", out, err)
		}
		if !strings.Contains(slurp(t, repo, target), "edited\n") {
			t.Fatal("the edit was destroyed")
		}
	})
	t.Run("a rendered block colliding with a foreign id is invalid", func(t *testing.T) {
		repo, m := twoGenerators(t)
		w, _ := NewWriter(repo, (&recorder{}).sink(), m, true)
		clash := mdFile("extra", "mine\n", "footer\n")
		out, err := w.Apply(context.Background(), Rendered{Path: target, GeneratorID: genA, Content: []byte(clash)})
		if k, _ := cascade.KindOf(err); err == nil || k != cascade.KindInvalidInput || out.Written {
			t.Fatalf("outcome = %+v %v", out, err)
		}
	})
}

func TestWriterApplyBlockJSONAppendUnsupported(t *testing.T) {
	repo, _ := newRepo(t)
	doc := "{\n  // cascade:managed:begin mcp\n  \"mcp\": {},\n  // cascade:managed:end\n  \"hand\": true\n}\n"
	m := generated(t, repo, Rendered{Path: "opencode.json", GeneratorID: genA, Content: []byte(doc)})
	before := snapshot(t, repo)
	rec := &recorder{}
	w, _ := NewWriter(repo, rec.sink(), m, true)
	_, err := w.ApplyBlock(context.Background(), "opencode.json", genB, "extra", []byte("  \"x\": 1,\n"))
	wantKind(t, err, cascade.KindUnsupported, "generate: cannot append block \"extra\" to opencode.json: no in-object anchor is defined")
	sameTree(t, before, snapshot(t, repo))
	if len(rec.calls) != 0 {
		t.Fatalf("sink called: %v", rec.calls)
	}
	out, err := w.ApplyBlock(context.Background(), "opencode.json", genA, "mcp", []byte("  \"mcp\": {\"a\": 1},\n"))
	if err != nil || !out.Written || !strings.Contains(slurp(t, repo, "opencode.json"), "\"a\": 1") {
		t.Fatalf("replacing an existing .json block must work: %+v %v", out, err)
	}
}

func TestWriterApplyBlockRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("unmanaged and no manifest", func(t *testing.T) {
		repo, _ := newRepo(t)
		put(t, repo, target, "hand\n")
		rec := &recorder{}
		w, _ := NewWriter(repo, rec.sink(), Manifest{}, false)
		out, err := w.ApplyBlock(ctx, target, genB, "b", []byte("x\n"))
		if err != nil || out.Reason != ReasonNoManifest || slurp(t, repo, target) != "hand\n" ||
			slurp(t, repo, target+".cascade-new") != "<!-- cascade:managed:begin b -->\nx\n<!-- cascade:managed:end -->\n" {
			t.Fatalf("outcome = %+v %v", out, err)
		}
		w2, _ := NewWriter(repo, rec.sink(), Manifest{Entries: []Entry{{Path: "other.md"}}}, true)
		if out, err := w2.ApplyBlock(ctx, target, genB, "b", nil); err != nil || out.Reason != ReasonUnmanagedFile {
			t.Fatalf("outcome = %+v %v", out, err)
		}
	})
	t.Run("outside edited", func(t *testing.T) {
		repo, _ := newRepo(t)
		m := seed(t, repo)
		put(t, repo, target, strings.Replace(baseFile(), "footer\n", "mine\n", 1))
		w, _ := NewWriter(repo, (&recorder{}).sink(), m, true)
		out, err := w.ApplyBlock(ctx, target, genA, "main", []byte("new\n"))
		if err != nil || out.Written || out.Reason != ReasonOutsideEdited || !strings.Contains(slurp(t, repo, target), "mine\n") {
			t.Fatalf("outcome = %+v %v", out, err)
		}
	})
	t.Run("another generator's block", func(t *testing.T) {
		repo, m := twoGenerators(t)
		w, _ := NewWriter(repo, (&recorder{}).sink(), m, true)
		_, err := w.ApplyBlock(ctx, target, "gen-c", "extra", []byte("x\n"))
		wantKind(t, err, cascade.KindConflict, "generate: block \"extra\" of AGENTS.md belongs to generator \"gen-b\"")
	})
}

func TestWriterApplyBlockInvalid(t *testing.T) {
	ctx := context.Background()
	t.Run("missing file and bad arguments", func(t *testing.T) {
		repo, _ := newRepo(t)
		w, _ := NewWriter(repo, (&recorder{}).sink(), Manifest{}, false)
		if _, err := w.ApplyBlock(ctx, target, genA, "b", nil); err == nil {
			t.Fatal("missing file accepted")
		} else if k, _ := cascade.KindOf(err); k != cascade.KindNotFound {
			t.Fatalf("kind = %v", k)
		}
		for _, bad := range [][3]string{{target, "", "b"}, {target, genA, "Bad"}, {"x.txt", genA, "b"}} {
			if _, err := w.ApplyBlock(ctx, bad[0], bad[1], bad[2], nil); err == nil {
				t.Fatalf("%v accepted", bad)
			}
		}
	})
	t.Run("body that corrupts the markers", func(t *testing.T) {
		repo, _ := newRepo(t)
		m := seed(t, repo)
		w, _ := NewWriter(repo, (&recorder{}).sink(), m, true)
		_, err := w.ApplyBlock(ctx, target, genB, "b", []byte("<!-- cascade:managed:begin q -->\n"))
		if k, _ := cascade.KindOf(err); err == nil || k != cascade.KindInvalidInput || slurp(t, repo, target) != baseFile() {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestWriterApplyBlockRaces(t *testing.T) {
	ctx := context.Background()
	t.Run("replacing with the same body is a no-op", func(t *testing.T) {
		repo, _ := newRepo(t)
		m := seed(t, repo)
		w, _ := NewWriter(repo, (&recorder{}).sink(), m, true)
		out, err := w.ApplyBlock(ctx, target, genA, "main", []byte("one\n"))
		if err != nil || !out.Adopted || out.Written {
			t.Fatalf("outcome = %+v %v", out, err)
		}
	})
	t.Run("changed inside the window", func(t *testing.T) {
		repo, _ := newRepo(t)
		m := seed(t, repo)
		edited := baseFile() + "late edit\n"
		beforePublish = func(string) { put(t, repo, target, edited) }
		t.Cleanup(func() { beforePublish = func(string) {} })
		w, _ := NewWriter(repo, (&recorder{}).sink(), m, true)
		out, err := w.ApplyBlock(ctx, target, genB, "b", []byte("x\n"))
		if err != nil || out.Written || out.Reason != ReasonOutsideEdited || slurp(t, repo, target) != edited {
			t.Fatalf("outcome = %+v %v", out, err)
		}
	})
}
