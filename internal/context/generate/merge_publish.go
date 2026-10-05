package generate

// Purpose: the publishing half of the Writer: the guarded write, the
//   sidecar a refusal leaves, and the path-escape refusal.
// Inputs: a judged file and the bytes to publish.
// Outputs: a published file, or an untouched target with a sidecar and one
//   AttentionSink call.
// Constraints: the sidecar goes through runtime.WriteFileAtomic and is
//   confined like any target. A sink error is returned after the sidecar
//   exists. The residual race window is described in merge.go.
// SPORT: context-engine/never-destroy-writer (ADD, P1-GEN-05).

import (
	"bytes"
	"context"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// beforePublish runs after the compare and before the re-read that guards
// the write; beforeCreate runs after the target was seen absent and before
// it is created. Production never swaps either; a test uses them to change
// the target inside those windows.
var (
	beforePublish = func(string) {}
	beforeCreate  = func(string) {}
)

// overwrite publishes content over data when the target still equals data.
func (w *Writer) overwrite(ctx context.Context, c *confined, r Rendered, form MarkerForm, data, content []byte, out Outcome) (Outcome, error) {
	e, err := entryFor(r.Path, r.GeneratorID, content, form, owners(w.next[r.Path]))
	if err != nil {
		return out, err
	}
	changed, err := publishIfUnchanged(c, data, content)
	if err != nil {
		return w.escaped(ctx, out, err)
	}
	if changed {
		return w.refuse(ctx, out, r.Path, r.Content, ReasonOutsideEdited)
	}
	w.next[r.Path], out.Written = e, true
	return out, nil
}

// publishIfUnchanged re-resolves and re-reads the target right before the
// write and publishes content only when it still equals base. changed is
// true when it did not, and nothing was written.
func publishIfUnchanged(c *confined, base, content []byte) (changed bool, err error) {
	beforePublish(c.rel)
	if err := c.walk(); err != nil {
		return false, err
	}
	cur, ok, err := c.read()
	if err != nil {
		return false, err
	}
	if !ok || !bytes.Equal(cur, base) {
		return true, nil
	}
	perm := c.mode.Perm()
	if perm == 0 {
		perm = 0o644
	}
	return false, runtime.WriteFileAtomic(c.abs, content, perm)
}

// refuse writes the sidecar, leaves the target alone and tells the sink.
func (w *Writer) refuse(ctx context.Context, out Outcome, rel string, sidecarBytes []byte, reason string) (Outcome, error) {
	sidecar := rel + sidecarSuffix
	sc, err := openConfined(w.root, sidecar)
	if err != nil {
		return w.escaped(ctx, out, err)
	}
	defer sc.close()
	if err := sc.ensureParent(); err != nil {
		return out, err
	}
	if err := runtime.WriteFileAtomic(sc.abs, sidecarBytes, 0o644); err != nil {
		return out, err
	}
	out.Sidecar, out.Reason = sidecar, reason
	if err := w.sink(ctx, rel, sidecar, reason); err != nil {
		return out, cascade.Wrapf(cascade.KindUnavailable, err, "generate: attention sink for %s", rel)
	}
	return out, nil
}

// escaped turns a confinement failure into a path-escape refusal: no
// sidecar, one sink call, the error returned. Any other error passes
// through.
func (w *Writer) escaped(ctx context.Context, out Outcome, err error) (Outcome, error) {
	if !isPathEscape(err) {
		return out, err
	}
	out.Reason = ReasonPathEscape
	if serr := w.sink(ctx, out.Path, "", ReasonPathEscape); serr != nil {
		return out, cascade.Wrapf(cascade.KindInvalidInput, err, "generate: attention sink failed: %v", serr)
	}
	return out, err
}
