package generate

// Purpose: the guards around a single-block edit. A block body is generated
//   text that may carry third-party content, so it must not be able to close
//   its own block, open another one or leave the markers it was given.
// Inputs: a body, or the content an edit produced and the spans before it.
// Outputs: nil, or an error of KindInvalidInput wrapping ErrMangledMarker.
// Constraints: a marker-shaped line is judged by the scanner's own grammar,
//   so indented, lone and malformed forms are all refused. The block set
//   after an edit must equal the set before it, plus the appended id.
// SPORT: context-engine/managed-blocks (ADD, P1-GEN-05).

import (
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// refuseMarkerBody refuses a body holding any marker-shaped line.
func refuseMarkerBody(form MarkerForm, id string, body []byte) error {
	s := &scanner{form: form}
	for n, line := range strings.Split(string(body), "\n") {
		if kind, _ := s.classify(strings.TrimSuffix(line, "\r")); kind != 't' {
			return cascade.Wrapf(cascade.KindInvalidInput, ErrMangledMarker,
				"generate: body of block %q line %d holds a managed-block marker", id, n+1)
		}
	}
	return nil
}

// verifyEdit scans the content an edit produced and refuses it when the
// markers broke or the block ids differ from before plus added ("" when
// nothing was appended).
func verifyEdit(id string, out []byte, form MarkerForm, before []span, added string) error {
	after, err := scanBlocks(out, form, nil)
	if err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err, "generate: body of block %q would corrupt the markers", id)
	}
	want := map[string]bool{}
	for _, sp := range before {
		want[sp.id] = true
	}
	if added != "" {
		want[added] = true
	}
	if len(after) != len(want) {
		return changedSet(id)
	}
	for _, sp := range after {
		if !want[sp.id] {
			return changedSet(id)
		}
	}
	return nil
}

// changedSet is the refusal for an edit that added or dropped a block.
func changedSet(id string) error {
	return cascade.Wrapf(cascade.KindInvalidInput, ErrMangledMarker,
		"generate: edit of block %q changed the set of managed blocks", id)
}
