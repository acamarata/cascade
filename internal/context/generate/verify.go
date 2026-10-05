package generate

// Purpose: compare a file on disk with the manifest entry that recorded
//   its last generation. The Writer and Check share these three questions.
// Inputs: the on-disk bytes, their blocks or spans and the recorded Entry.
// Outputs: whether a managed block was edited, and whether anything
//   outside the blocks (or the file as a whole) differs from the base.
// Constraints: pure functions over bytes; nothing here reads or writes a
//   file.
// SPORT: context-engine/generation-verify (ADD, P1-GEN-05).

// knownIDs is the closed set of block ids the entry recorded.
func (e Entry) knownIDs() map[string]bool {
	ids := make(map[string]bool, len(e.Blocks))
	for _, b := range e.Blocks {
		ids[b.ID] = true
	}
	return ids
}

// blocksEdited reports whether any recorded block is missing from blocks
// or carries a body whose hash differs from the recorded one.
func blocksEdited(blocks []Block, e Entry) bool {
	have := make(map[string]string, len(blocks))
	for _, b := range blocks {
		have[b.ID] = sha256Hex(b.Body)
	}
	for _, b := range e.Blocks {
		if h, ok := have[b.ID]; !ok || h != b.ContentSHA256 {
			return true
		}
	}
	return false
}

// bodiesOf turns spans into the blocks they delimit.
func bodiesOf(content []byte, spans []span) []Block {
	blocks := make([]Block, 0, len(spans))
	for _, sp := range spans {
		blocks = append(blocks, Block{ID: sp.id, Body: content[sp.bodyStart:sp.bodyEnd]})
	}
	return blocks
}

// baseEdited reports whether the file is not byte-for-byte the recorded
// base once its blocks are known to match: either the bytes outside the
// blocks changed, or the file hash differs (blocks moved or reordered).
func baseEdited(content []byte, spans []span, e Entry) bool {
	return sha256Hex(outsideBytes(content, spans)) != e.OutsideSHA256 || sha256Hex(content) != e.FileSHA256
}
