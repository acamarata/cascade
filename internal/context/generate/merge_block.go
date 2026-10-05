package generate

// Purpose: block-level Apply for a later projector. A managed file can
//   take one more generator's block without becoming "outside-edited".
// Inputs: a managed file's path, the block's generator and id, its body.
// Outputs: an Outcome like Apply's. The block is replaced in place, or
//   appended at the end of a .md, .toml or .js target.
// Constraints: the file must be managed and pass the same checks as
//   Apply. Appending to a .json or .jsonc target is KindUnsupported until
//   an in-object anchor is defined, and leaves the file byte-identical.
//   A block id owned by another generator is KindConflict. The entry's
//   outside hash is recomputed from the new bytes; it only moves when an
//   LF had to be added before the appended block.
// SPORT: context-engine/never-destroy-writer (ADD, P1-GEN-05).

import (
	"bytes"
	"context"
	pathpkg "path"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ApplyBlock replaces or appends one managed block of a managed file.
func (w *Writer) ApplyBlock(ctx context.Context, path, generatorID, blockID string, body []byte) (Outcome, error) {
	out := Outcome{Path: path}
	if err := ctx.Err(); err != nil {
		return out, cascade.Wrap(cascade.KindCanceled, err, "generate: apply block canceled")
	}
	if generatorID == "" || !blockIDPattern.MatchString(blockID) {
		return out, cascade.Newf(cascade.KindInvalidInput, "generate: invalid block %q of generator %q", blockID, generatorID)
	}
	c, err := openConfined(w.root, path)
	if err != nil {
		return w.escaped(ctx, out, err)
	}
	defer c.close()
	form, err := FormFor(path)
	if err != nil {
		return out, err
	}
	data, exists, err := c.read()
	if err != nil {
		return out, err
	}
	if !exists {
		return out, cascade.Newf(cascade.KindNotFound, "generate: %s does not exist; blocks go into managed files only", path)
	}
	content, reason, err := w.planBlock(path, generatorID, blockID, form, data, body)
	if err != nil {
		return out, err
	}
	if reason != "" {
		return w.refuse(ctx, out, path, WrapBlock(form, blockID, body), reason)
	}
	if bytes.Equal(content, data) {
		out.Adopted = true
		return out, nil
	}
	req := blockReq{path: path, generatorID: generatorID, blockID: blockID, form: form, body: body}
	return w.publishBlock(ctx, c, req, data, content, out)
}

// blockReq is one ApplyBlock request after validation.
type blockReq struct {
	path, generatorID, blockID string
	form                       MarkerForm
	body                       []byte
}

// publishBlock writes content and records the new entry.
func (w *Writer) publishBlock(ctx context.Context, c *confined, q blockReq, data, content []byte, out Outcome) (Outcome, error) {
	prev := w.next[q.path]
	own := owners(prev)
	own[q.blockID] = q.generatorID
	e, err := entryFor(q.path, prev.GeneratorID, content, q.form, own)
	if err != nil {
		return out, err
	}
	changed, err := publishIfUnchanged(c, data, content)
	if err != nil {
		return w.escaped(ctx, out, err)
	}
	if changed {
		return w.refuse(ctx, out, q.path, WrapBlock(q.form, q.blockID, q.body), ReasonOutsideEdited)
	}
	w.next[q.path], out.Written = e, true
	return out, nil
}

// planBlock checks data against the recorded base and builds the new
// content, or names the refusal reason.
func (w *Writer) planBlock(path, generatorID, blockID string, form MarkerForm, data, body []byte) ([]byte, string, error) {
	e, managed := w.next[path]
	switch {
	case !managed && !w.hadPrior:
		return nil, ReasonNoManifest, nil
	case !managed:
		return nil, ReasonUnmanagedFile, nil
	}
	reason, spans := verifyBase(data, form, e)
	if reason != "" {
		return nil, reason, nil
	}
	for _, b := range e.Blocks {
		if b.ID == blockID && b.GeneratorID != generatorID {
			return nil, "", cascade.Newf(cascade.KindConflict, "generate: block %q of %s belongs to generator %q", blockID, path, b.GeneratorID)
		}
	}
	for _, sp := range spans {
		if sp.id == blockID {
			content, err := ReplaceManagedBlock(data, form, blockID, body)
			return content, "", err
		}
	}
	return appendBlock(path, form, blockID, data, body)
}

// appendBlock adds the block at the end of the file.
func appendBlock(path string, form MarkerForm, blockID string, data, body []byte) ([]byte, string, error) {
	if ext := strings.ToLower(pathpkg.Ext(path)); ext == ".json" || ext == ".jsonc" {
		return nil, "", cascade.Newf(cascade.KindUnsupported, "generate: cannot append block %q to %s: no in-object anchor is defined", blockID, path)
	}
	if err := refuseMarkerBody(form, blockID, body); err != nil {
		return nil, "", err
	}
	before, err := scanBlocks(data, form, nil)
	if err != nil {
		return nil, "", err
	}
	content := append(append([]byte(nil), lfTerminated(data)...), WrapBlock(form, blockID, body)...)
	if err := verifyEdit(blockID, content, form, before, blockID); err != nil {
		return nil, "", err
	}
	return content, "", nil
}
