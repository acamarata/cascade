// Package pews (author_id.go): canonical-id parsing and tree-position
// resolution shared by author.go's Create/Edit/Move. Split into its own
// file, mirroring validate.go/validate_ids.go and lint.go/lint_rules.go's
// existing concern-split in this same plugin, because author.go plus these
// helpers together would exceed the repo's 300-line-per-file gate.
// SPORT: plugins/pbd/internal/pews author (ADD) — P1-E14-W3-S28-T3.
package pews

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/acamarata/cascade/pkg/cascade"
)

// canonicalIDRe parses a canonical ticket id into its five components. A
// wrongly-padded id (e.g. "P1-E5-..." instead of "P1-E05-...") still
// fails parseCanonicalID's round trip through canonicalID.
var canonicalIDRe = regexp.MustCompile(`^([A-Za-z0-9]+)-E([0-9]+)-W([0-9]+)-S([0-9]+)-T([0-9]+)$`)

// idComponents is one canonical ticket id decomposed into the fields
// relPathFor and canonicalID both need.
type idComponents struct {
	phase                         string
	epicNum, wave, sprint, ticket int
}

// parseCanonicalID decomposes id, requiring a round trip through
// canonicalID to reproduce id verbatim (the same discipline
// identityViolations in validate.go enforces on a loaded tree).
func parseCanonicalID(id string) (idComponents, error) {
	m := canonicalIDRe.FindStringSubmatch(id)
	if m == nil {
		return idComponents{}, cascade.Newf(cascade.KindInvalidInput, "pews: %q is not a canonical ticket id", id)
	}
	epicNum, _ := strconv.Atoi(m[2])
	wave, _ := strconv.Atoi(m[3])
	sprint, _ := strconv.Atoi(m[4])
	ticket, _ := strconv.Atoi(m[5])
	c := idComponents{phase: m[1], epicNum: epicNum, wave: wave, sprint: sprint, ticket: ticket}
	if canonicalID(c.phase, c.epicNum, c.wave, c.sprint, c.ticket) != id {
		return idComponents{}, cascade.Newf(cascade.KindInvalidInput, "pews: %q does not use the canonical zero-padded id form", id)
	}
	return c, nil
}

// lettersFromEpicNumber inverts epicNumberFromLetters (store.go): bijective
// base-26, so 1 -> "A", 26 -> "Z", 27 -> "AA".
func lettersFromEpicNumber(n int) string {
	var b []byte
	for n > 0 {
		n--
		b = append([]byte{byte('A' + n%26)}, b...)
		n /= 26
	}
	return string(b)
}

// relPathFor returns c's canonical ticket path relative to a tree root, in
// the directory shape store.go's loader walks.
func relPathFor(c idComponents) string {
	return filepath.Join(
		"epics", "E-"+lettersFromEpicNumber(c.epicNum),
		"waves", fmt.Sprintf("W-%d", c.wave),
		"sprints", fmt.Sprintf("S-%02d", c.sprint),
		"tickets", fmt.Sprintf("T-%d.yaml", c.ticket),
	)
}

// recordFor builds t's TicketRecord once its id resolves to c at relPath.
func recordFor(t Ticket, c idComponents, relPath string) TicketRecord {
	return TicketRecord{
		ID: t.ID, CanonicalID: canonicalID(c.phase, c.epicNum, c.wave, c.sprint, c.ticket),
		Ticket: t, RelPath: relPath, EpicLetters: lettersFromEpicNumber(c.epicNum),
		EpicNum: c.epicNum, Wave: c.wave, Sprint: c.sprint, TicketNum: c.ticket,
	}
}

// requirePhase refuses c whenever its phase segment does not match phase.
func requirePhase(c idComponents, phase, id string) error {
	if c.phase != phase {
		return cascade.Newf(cascade.KindInvalidInput,
			"pews: ticket id %q carries phase %q, does not match tree phase %q", id, c.phase, phase)
	}
	return nil
}
