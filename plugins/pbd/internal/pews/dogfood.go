// Package pews (dogfood.go): Purpose: the Epic N dogfood ENGINE
// CONVERSION driver (P1-E14-W3-S30-T3, 06-FORGE-SPEC §6.4) — load an
// already-forged PEWS YAML tree through the S-28.T2 store, run the T2
// structural validator and the S-28.T4 contract lint to zero issues (via
// Lint, which composes Validate as its own precondition), and re-emit the
// tree through the store into a scratch destination, proving a
// byte-identical, idempotent round-trip. Reuses the T2 loader and its
// dependency handling verbatim: no plan-Markdown parser, no dep-grammar
// re-resolver, no new CLI verb.
//
// Inputs: src/dst directory paths (dst is always a t.TempDir() or other
// caller-supplied scratch target, R-14.45 — never the repo root, Art.
// 10.1 Clean Root). Outputs: a ConvertResult (ticket count and the
// idempotency delta), or a *cascade.Error. Convert never skips a bad
// ticket: NewStore.Load's own fail-closed contract (a decode failure
// refuses the WHOLE load, never a partial Tree) is what makes "an
// unparseable ticket is refused, not silently dropped" true here, without
// any per-ticket try/skip logic of this file's own.
//
// Constraints: no bare time.Now/rand; deterministic (tree.Tickets' own
// sorted-by-walk order); no network. Emission never happens outside dst.
//
// SPORT: plugins/pbd/internal/pews dogfood-convert (ADD) — P1-E14-W3-S30-T3.
package pews

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/acamarata/cascade/pkg/cascade"
)

// dogfoodPhase is the ticket-id phase prefix Convert loads and re-emits
// under. This ticket dogfoods THIS plan specifically (P1), so Convert's
// two-argument (src, dst string) signature — the contract's own literal
// shape — carries no separate phase parameter; a future ticket generalizing
// this driver to another phase can add one without changing this behavior.
const dogfoodPhase = "P1"

// ConvertResult summarizes one Convert call.
type ConvertResult struct {
	// TicketCount is how many tickets src's tree held (validator/lint
	// already refused anything short of every one of them being
	// structurally sound and contract-clean).
	TicketCount int
	// Changed reports whether re-emission wrote anything different from
	// what dst already held — false on a fully converged (idempotent)
	// re-run.
	Changed bool
	// ChangedFiles lists the relative paths Convert actually wrote,
	// sorted. Empty (with Changed false) is the "no changes" delta.
	ChangedFiles []string
}

// Convert loads src's PEWS tree, requires it to lint clean (which itself
// requires it to validate clean, lint.go's own composed precondition),
// and re-emits every ticket plus any tombstones.yaml into dst, skipping a
// file whose content already matches byte-for-byte. It never mutates src.
func Convert(src, dst string) (ConvertResult, error) {
	tree, err := NewStore(src, dogfoodPhase).Load()
	if err != nil {
		return ConvertResult{}, err
	}
	if _, lerr := Lint(tree); lerr != nil {
		return ConvertResult{}, lerr
	}
	changed, eerr := emitTree(dst, tree)
	if eerr != nil {
		return ConvertResult{}, eerr
	}
	return ConvertResult{TicketCount: len(tree.Tickets), Changed: len(changed) > 0, ChangedFiles: changed}, nil
}

// emitTree writes every ticket in tree to its canonical relative position
// under dst, plus tombstones.yaml when tree carries any, returning the
// sorted list of relative paths actually changed.
func emitTree(dst string, tree *Tree) ([]string, error) {
	var changed []string
	for _, rec := range tree.Tickets {
		data, eerr := EncodeTicket(rec.Ticket)
		if eerr != nil {
			return nil, eerr
		}
		absPath := filepath.Join(dst, rec.RelPath)
		same, serr := sameFileContent(absPath, data)
		if serr != nil {
			return nil, serr
		}
		if same {
			continue
		}
		if werr := atomicWriteTicket(absPath, rec.Ticket); werr != nil {
			return nil, werr
		}
		changed = append(changed, rec.RelPath)
	}
	if len(tree.Tombstones) > 0 {
		tombChanged, terr := emitTombstones(dst, tree.Tombstones)
		if terr != nil {
			return nil, terr
		}
		if tombChanged {
			changed = append(changed, "tombstones.yaml")
		}
	}
	sort.Strings(changed)
	return changed, nil
}

// emitTombstones re-emits tombs into dst/tombstones.yaml, reusing
// residue.go's marshalSidecar (this package's one tombstones.yaml writer)
// unless the content already matches byte-for-byte.
func emitTombstones(dst string, tombs []Tombstone) (bool, error) {
	doc := struct {
		Tombstones []Tombstone `yaml:"tombstones"`
	}{Tombstones: tombs}
	data, merr := yaml.Marshal(doc)
	if merr != nil {
		return false, cascade.Wrap(cascade.KindInternal, merr, "pews: encoding tombstones YAML")
	}
	path := filepath.Join(dst, "tombstones.yaml")
	same, serr := sameFileContent(path, data)
	if serr != nil {
		return false, serr
	}
	if same {
		return false, nil
	}
	if werr := marshalSidecar(dst, "tombstones.yaml", doc); werr != nil {
		return false, werr
	}
	return true, nil
}

// sameFileContent reports whether path already holds exactly data. A
// missing path is "not the same" (false, nil), never an error: the first
// emission into a fresh dst always writes.
func sameFileContent(path string, data []byte) (bool, error) {
	existing, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, cascade.Wrapf(cascade.KindInternal, err, "pews: reading %q", path)
	}
	return bytes.Equal(existing, data), nil
}
