package generate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	genA   = "gen-a"
	target = "AGENTS.md"
)

// baseFile and newFile are two renderings of the same generated file.
func baseFile() string { return mdFile("main", "one\n", "footer\n") }
func newFile() string  { return mdFile("main", "two\n", "footer\n") }

// seed creates target from baseFile with a first Writer and returns the
// manifest a later Writer treats as the previous generation.
func seed(t *testing.T, repo string) Manifest {
	t.Helper()
	return generated(t, repo, Rendered{Path: target, GeneratorID: genA, Content: []byte(baseFile())})
}

// applyNew runs a fresh Writer over prior and applies newFile.
func applyNew(t *testing.T, repo string, prior Manifest, had bool, rec *recorder) (Outcome, error) {
	t.Helper()
	w, err := NewWriter(repo, rec.sink(), prior, had)
	if err != nil {
		t.Fatal(err)
	}
	return w.Apply(context.Background(), Rendered{Path: target, GeneratorID: genA, Content: []byte(newFile())})
}

// sidecarCase is one way a target can differ from the recorded base.
type sidecarCase struct {
	name, reason string
	prior        func(Manifest) (Manifest, bool)
	disk         string
}

func sidecarCases() []sidecarCase {
	begin, end := "<!-- cascade:managed:begin main -->\n", "<!-- cascade:managed:end -->\n"
	return []sidecarCase{
		{"no manifest", ReasonNoManifest, func(Manifest) (Manifest, bool) { return Manifest{}, false }, "hand written\n"},
		{"unmanaged file", ReasonUnmanagedFile, func(m Manifest) (Manifest, bool) {
			m.Entries[0].Path = "OTHER.md"
			return m, true
		}, "hand written\n"},
		{"unmatched begin", ReasonMangledMarker, nil, strings.Replace(baseFile(), end, "", 1)},
		{"unmatched end", ReasonMangledMarker, nil, baseFile() + end},
		{"duplicate id", ReasonMangledMarker, nil, baseFile() + begin + "dup\n" + end},
		{"unknown id", ReasonMangledMarker, nil, baseFile() + strings.Replace(begin, "main", "extra", 1) + "x\n" + end},
		{"managed block edited", ReasonBlockEdited, nil, strings.Replace(baseFile(), "one\n", "edited\n", 1)},
		{"outside edited", ReasonOutsideEdited, nil, strings.Replace(baseFile(), "footer\n", "my own footer\n", 1)},
	}
}

func TestMergeRefusesAndWritesSidecar(t *testing.T) {
	for _, c := range sidecarCases() {
		t.Run(c.name, func(t *testing.T) {
			repo, _ := newRepo(t)
			prior := seed(t, repo)
			had := true
			if c.prior != nil {
				prior, had = c.prior(prior)
			}
			put(t, repo, target, c.disk)
			before := snapshot(t, repo)
			rec := &recorder{}
			out, err := applyNew(t, repo, prior, had, rec)
			if err != nil {
				t.Fatal(err)
			}
			if out.Written || out.Adopted || out.Reason != c.reason || out.Sidecar != target+".cascade-new" {
				t.Fatalf("outcome = %+v", out)
			}
			if got := slurp(t, repo, target); got != c.disk {
				t.Fatalf("target changed:\n%q", got)
			}
			if got := slurp(t, repo, target+".cascade-new"); got != newFile() {
				t.Fatalf("sidecar = %q", got)
			}
			if len(rec.calls) != 1 || rec.calls[0] != (sinkCall{target, target + ".cascade-new", c.reason}) {
				t.Fatalf("sink calls = %+v", rec.calls)
			}
			delete(before, target+".cascade-new")
			after := snapshot(t, repo)
			delete(after, target+".cascade-new")
			sameTree(t, before, after)
		})
	}
}

func TestMergeRewritesOnlyTheRecordedBase(t *testing.T) {
	repo, _ := newRepo(t)
	prior := seed(t, repo)
	if err := os.Chmod(filepath.Join(repo, target), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	w, err := NewWriter(repo, rec.sink(), prior, true)
	if err != nil {
		t.Fatal(err)
	}
	out, err := w.Apply(context.Background(), Rendered{Path: target, GeneratorID: genA, Content: []byte(newFile())})
	if err != nil || !out.Written || out.Reason != "" || len(rec.calls) != 0 {
		t.Fatalf("outcome = %+v %v calls=%v", out, err, rec.calls)
	}
	if slurp(t, repo, target) != newFile() || !missing(repo, target+".cascade-new") {
		t.Fatal("base was not rewritten cleanly")
	}
	if info, _ := os.Stat(filepath.Join(repo, target)); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want the existing 0600 kept", info.Mode().Perm())
	}
	m := w.Manifest(fixedNow, nil)
	if len(m.Entries) != 1 || m.Entries[0].FileSHA256 != sum(newFile()) {
		t.Fatalf("manifest = %+v", m)
	}
}

func TestMissingManifestTreatsAllHandAuthored(t *testing.T) {
	repo, _ := newRepo(t)
	put(t, repo, "CLAUDE.md", "hand\n")
	rec := &recorder{}
	w, err := NewWriter(repo, rec.sink(), Manifest{}, false)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := w.Apply(context.Background(), Rendered{Path: ".claude/rules/root.md", GeneratorID: genA, Content: []byte(baseFile())})
	if err != nil || !fresh.Written || fresh.Reason != "" {
		t.Fatalf("absent target: %+v %v", fresh, err)
	}
	if slurp(t, repo, ".claude/rules/root.md") != baseFile() {
		t.Fatal("absent target was not created")
	}
	old, err := w.Apply(context.Background(), Rendered{Path: "CLAUDE.md", GeneratorID: genA, Content: []byte(baseFile())})
	if err != nil || old.Written || old.Reason != ReasonNoManifest || old.Sidecar != "CLAUDE.md.cascade-new" {
		t.Fatalf("existing target: %+v %v", old, err)
	}
	if slurp(t, repo, "CLAUDE.md") != "hand\n" || slurp(t, repo, "CLAUDE.md.cascade-new") != baseFile() {
		t.Fatal("hand-authored target was touched or the sidecar is wrong")
	}
	if len(rec.calls) != 1 || rec.calls[0].Target != "CLAUDE.md" {
		t.Fatalf("sink calls = %+v", rec.calls)
	}
	if m := w.Manifest(fixedNow, nil); len(m.Entries) != 1 || m.Entries[0].Path != ".claude/rules/root.md" {
		t.Fatalf("manifest must hold only the created file: %+v", m.Entries)
	}
}

func TestMergeNilAttentionSinkRefused(t *testing.T) {
	w, err := NewWriter(t.TempDir(), nil, Manifest{}, false)
	if w != nil {
		t.Fatal("writer returned with a nil sink")
	}
	wantKind(t, err, cascade.KindInvalidInput, "generate: nil AttentionSink")
}

func TestMergeCallsAttentionSinkOnRefusal(t *testing.T) {
	repo, _ := newRepo(t)
	prior := seed(t, repo)
	put(t, repo, target, "hand\n")
	rec := &recorder{err: cascade.New(cascade.KindUnavailable, "sink down")}
	out, err := applyNew(t, repo, prior, false, rec)
	if err == nil || out.Reason != ReasonNoManifest {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != cascade.KindUnavailable || ce.Msg != "generate: attention sink for AGENTS.md" ||
		ce.Err != rec.err {
		t.Fatalf("sink error not returned intact: %#v", err)
	}
	if slurp(t, repo, target+".cascade-new") != newFile() || slurp(t, repo, target) != "hand\n" {
		t.Fatal("sidecar must exist and the target must be untouched when the sink fails")
	}
	if len(rec.calls) != 1 {
		t.Fatalf("sink called %d times, want exactly once", len(rec.calls))
	}
}

func TestWriterAdoptsIdenticalTarget(t *testing.T) {
	repo, _ := newRepo(t)
	put(t, repo, target, baseFile())
	before, _ := os.Stat(filepath.Join(repo, target))
	rec := &recorder{}
	w, err := NewWriter(repo, rec.sink(), Manifest{}, false)
	if err != nil {
		t.Fatal(err)
	}
	r := Rendered{Path: target, GeneratorID: genA, Content: []byte(baseFile())}
	out, err := w.Apply(context.Background(), r)
	if err != nil || !out.Adopted || out.Written || out.Reason != "" || len(rec.calls) != 0 {
		t.Fatalf("outcome = %+v %v", out, err)
	}
	after, _ := os.Stat(filepath.Join(repo, target))
	if !os.SameFile(before, after) {
		t.Fatal("an identical target was rewritten")
	}
	m := w.Manifest(fixedNow, nil)
	if len(m.Entries) != 1 || m.Entries[0].FileSHA256 != sum(baseFile()) {
		t.Fatalf("adopted entry not recorded: %+v", m)
	}
	w2, _ := NewWriter(repo, rec.sink(), m, true)
	again, err := w2.Apply(context.Background(), r)
	if err != nil || again.Written || !again.Adopted {
		t.Fatalf("second generation must be a no-op: %+v %v", again, err)
	}
}

func TestWriterRehashBeforeRename(t *testing.T) {
	repo, _ := newRepo(t)
	prior := seed(t, repo)
	edited := strings.Replace(baseFile(), "footer\n", "edited in the window\n", 1)
	beforePublish = func(string) { put(t, repo, target, edited) }
	t.Cleanup(func() { beforePublish = func(string) {} })
	rec := &recorder{}
	out, err := applyNew(t, repo, prior, true, rec)
	if err != nil || out.Written || out.Reason != ReasonOutsideEdited {
		t.Fatalf("outcome = %+v %v", out, err)
	}
	if slurp(t, repo, target) != edited {
		t.Fatal("a target changed between compare and rename was overwritten")
	}
	if slurp(t, repo, target+".cascade-new") != newFile() || len(rec.calls) != 1 {
		t.Fatalf("sidecar/sink missing: %v", rec.calls)
	}
}

func TestMergeLostCreationRaceIsJudgedAsPresent(t *testing.T) {
	repo, _ := newRepo(t)
	beforeCreate = func(string) { put(t, repo, target, "raced in\n") }
	t.Cleanup(func() { beforeCreate = func(string) {} })
	rec := &recorder{}
	out, err := applyNew(t, repo, Manifest{}, false, rec)
	if err != nil || out.Written || out.Reason != ReasonNoManifest {
		t.Fatalf("outcome = %+v %v", out, err)
	}
	if slurp(t, repo, target) != "raced in\n" {
		t.Fatal("the racing writer's file was replaced")
	}
}

func TestMergeRejectsBadInput(t *testing.T) {
	repo, _ := newRepo(t)
	rec := &recorder{}
	w, _ := NewWriter(repo, rec.sink(), Manifest{}, false)
	ctx := context.Background()
	for name, r := range map[string]Rendered{
		"extension":   {Path: "notes.txt", GeneratorID: genA, Content: []byte("x")},
		"no id":       {Path: target, Content: []byte(baseFile())},
		"bad content": {Path: target, GeneratorID: genA, Content: []byte("<!-- cascade:managed:begin a -->\n")},
	} {
		if out, err := w.Apply(ctx, r); err == nil || out.Written {
			t.Fatalf("%s accepted: %+v", name, out)
		} else if k, _ := cascade.KindOf(err); k != cascade.KindInvalidInput {
			t.Fatalf("%s kind = %v", name, k)
		}
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := w.Apply(cctx, Rendered{Path: target, GeneratorID: genA, Content: []byte(baseFile())}); err == nil {
		t.Fatal("canceled context accepted")
	} else if k, _ := cascade.KindOf(err); k != cascade.KindCanceled {
		t.Fatalf("kind = %v", k)
	}
	if !missing(repo, target) || len(rec.calls) != 0 {
		t.Fatal("a rejected input wrote or notified")
	}
}
