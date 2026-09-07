// Package pews (author.go): native authoring engine N/S-28.T3 owns —
// Create, Edit, Move, backing `pbd create|edit|move`. Each is pre-flighted
// against the SAME Lint (composes Validate) the read path runs, in
// memory, before a byte touches disk: a call that would leave the tree
// unsound or contract-incomplete writes nothing. The one write each
// performs is atomic (temp file, then rename); a failure partway through
// never leaves a partial ticket file behind. Canonical-id parsing and
// tree-position resolution live in author_id.go.
// Inputs: a tree root/phase plus a Ticket (Create, Edit) or a pair of
// canonical ids (Move); no clock, no network.
// Outputs: nil on success; *cascade.Error otherwise — KindConflict for
// no-clobber, KindNotFound for a missing target, KindInvalidInput for a
// malformed id or a Lint-refused candidate, propagated verbatim.
// Constraints: tests root every tree at t.TempDir() (Art.7); no bare
// time.Now; a refused call never writes or removes a file.
// SPORT: plugins/pbd/internal/pews author (ADD) — P1-E14-W3-S28-T3.
package pews

import (
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// checkCandidate runs Lint (composes Validate) over the proposed tree. For
// a draft tree it drops any LintKindCRWeightMismatch issue before deciding
// pass/fail (filterDraftLintIssues, draft.go) — R-21.276's "a draft phase
// accepts ticket edits without weight/model validation" applied to the one
// Lint rule that re-derives a weight-shaped constraint; every other issue
// still refuses. A non-draft tree's result passes through verbatim.
func checkCandidate(tree *Tree) error {
	report, err := Lint(tree)
	if err == nil || !tree.Draft {
		return err
	}
	filtered := filterDraftLintIssues(report.Issues)
	if len(filtered) == 0 {
		return nil
	}
	return cascade.Newf(cascade.KindInvalidInput, "pews: %d contract-lint issue(s) found", len(filtered))
}

// Create authors a brand-new ticket at t.ID's canonical position. It
// writes nothing when: t.ID does not parse or match phase; a ticket file
// already exists there (KindConflict — use Edit); or adding t to the tree
// would fail Lint.
func Create(root, phase string, t Ticket) error {
	absPath, err := prepareWrite(root, phase, t, false)
	if err != nil {
		return err
	}
	return atomicWriteTicket(absPath, t)
}

// Edit overwrites the existing ticket at t.ID's canonical position. It
// writes nothing when: t.ID does not parse or match phase; no ticket
// exists there yet (KindNotFound — use Create); or the replacement would
// fail Lint.
func Edit(root, phase string, t Ticket) error {
	absPath, err := prepareWrite(root, phase, t, true)
	if err != nil {
		return err
	}
	return atomicWriteTicket(absPath, t)
}

// prepareWrite is Create/Edit's shared preflight: parse t.ID, load the
// tree, resolve t.ID's path, check the existence precondition (false for
// Create, true for Edit), build the candidate tree with t's record, and
// Lint it. Returns the absolute path to write atomically; writes nothing.
func prepareWrite(root, phase string, t Ticket, wantExists bool) (string, error) {
	c, err := parseCanonicalID(t.ID)
	if err != nil {
		return "", err
	}
	if err := requirePhase(c, phase, t.ID); err != nil {
		return "", err
	}
	// Authoring always sees a draft phase's own tickets (IncludeDrafts:
	// true) — a draft is edited normally, only its weight/model
	// validation is relaxed (checkCandidate below); the isolation
	// R-21.276 requires is for ACTIVE reads, not for authoring itself.
	tree, err := NewStore(root, phase).LoadWithOptions(LoadOptions{IncludeDrafts: true})
	if err != nil {
		return "", err
	}
	relPath := relPathFor(c)
	absPath := filepath.Join(root, relPath)
	exists, err := fileExists(absPath)
	if err != nil {
		return "", err
	}
	if exists != wantExists {
		return "", existenceError(wantExists, relPath)
	}
	canonID := canonicalID(c.phase, c.epicNum, c.wave, c.sprint, c.ticket)
	tickets := make([]TicketRecord, 0, len(tree.Tickets)+1)
	for _, r := range tree.Tickets {
		if r.CanonicalID != canonID {
			tickets = append(tickets, r)
		}
	}
	tickets = append(tickets, recordFor(t, c, relPath))
	candidate := &Tree{Phase: tree.Phase, Draft: tree.Draft, Tombstones: tree.Tombstones, Tickets: tickets}
	if err := checkCandidate(candidate); err != nil {
		return "", err
	}
	return absPath, nil
}

// existenceError renders Create/Edit's precondition refusal: wantExists
// true is Edit's "must already exist", false is Create's "must not".
func existenceError(wantExists bool, relPath string) error {
	if wantExists {
		return cascade.Newf(cascade.KindNotFound, "pews: no ticket exists at %q; use create, not edit", relPath)
	}
	return cascade.Newf(cascade.KindConflict, "pews: a ticket already exists at %q; use edit, not create", relPath)
}

// Move relocates the ticket at fromID's canonical position to toID's,
// updating its declared id to match toID. It writes and removes nothing
// when: an id does not parse or match phase; fromID equals toID; no
// ticket exists at fromID (KindNotFound); a ticket already exists at toID
// (KindConflict — no clobber); or the moved tree would fail Lint.
func Move(root, phase, fromID, toID string) error {
	fromC, toC, err := parseMoveIDs(phase, fromID, toID)
	if err != nil {
		return err
	}
	tree, err := NewStore(root, phase).LoadWithOptions(LoadOptions{IncludeDrafts: true})
	if err != nil {
		return err
	}
	fromPath := relPathFor(fromC)
	fromAbs := filepath.Join(root, fromPath)
	data, rerr := os.ReadFile(fromAbs)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return cascade.Newf(cascade.KindNotFound, "pews: no ticket exists at %q to move", fromPath)
		}
		return cascade.Wrapf(cascade.KindInternal, rerr, "pews: reading %q", fromAbs)
	}
	decode := ticketDecoder(DecodeTicket)
	if tree.Draft {
		decode = DecodeDraftTicket
	}
	t, derr := decode(data)
	if derr != nil {
		return cascade.Wrap(cascade.KindInvalidInput, derr, "pews: decoding ticket at move source")
	}
	toPath := relPathFor(toC)
	toAbs := filepath.Join(root, toPath)
	if exists, err := fileExists(toAbs); err != nil {
		return err
	} else if exists {
		return cascade.Newf(cascade.KindConflict, "pews: a ticket already exists at %q; move refuses to clobber", toPath)
	}
	t.ID = toID
	candidate := moveCandidateTree(tree, t, fromID, toC, toPath)
	if err := checkCandidate(candidate); err != nil {
		return err
	}
	if err := atomicWriteTicket(toAbs, t); err != nil {
		return err
	}
	if rmErr := os.Remove(fromAbs); rmErr != nil {
		return cascade.Wrapf(cascade.KindInternal, rmErr, "pews: removing old ticket file %q after move", fromAbs)
	}
	return nil
}

// parseMoveIDs parses and phase-checks Move's fromID/toID pair, and
// refuses when they are equal.
func parseMoveIDs(phase, fromID, toID string) (idComponents, idComponents, error) {
	if fromID == toID {
		return idComponents{}, idComponents{}, cascade.New(cascade.KindInvalidInput, "pews: move requires distinct source and target ids")
	}
	fromC, err := parseCanonicalID(fromID)
	if err != nil {
		return idComponents{}, idComponents{}, err
	}
	toC, err := parseCanonicalID(toID)
	if err != nil {
		return idComponents{}, idComponents{}, err
	}
	if err := requirePhase(fromC, phase, fromID); err != nil {
		return idComponents{}, idComponents{}, err
	}
	if err := requirePhase(toC, phase, toID); err != nil {
		return idComponents{}, idComponents{}, err
	}
	return fromC, toC, nil
}

// moveCandidateTree drops the fromID record and adds t's new one.
func moveCandidateTree(tree *Tree, t Ticket, fromID string, toC idComponents, toPath string) *Tree {
	tickets := make([]TicketRecord, 0, len(tree.Tickets))
	for _, r := range tree.Tickets {
		if r.CanonicalID != fromID {
			tickets = append(tickets, r)
		}
	}
	tickets = append(tickets, recordFor(t, toC, toPath))
	return &Tree{Phase: tree.Phase, Draft: tree.Draft, Tombstones: tree.Tombstones, Tickets: tickets}
}

// fileExists distinguishes "does not exist" (false, nil) from a real stat
// failure (false, err).
func fileExists(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, cascade.Wrapf(cascade.KindInternal, err, "pews: checking %q", path)
	}
	return true, nil
}

// atomicWriteTicket encodes t to a temp file beside absPath, syncs, closes,
// then renames over the target; a failure before the rename removes the
// temp file and leaves absPath untouched, and rename(2) is itself atomic.
func atomicWriteTicket(absPath string, t Ticket) error {
	data, err := EncodeTicket(t)
	if err != nil {
		return err
	}
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return cascade.Wrapf(cascade.KindInternal, err, "pews: creating directory %q", dir)
	}
	tmp, err := os.CreateTemp(dir, ".ticket-*.yaml.tmp")
	if err != nil {
		return cascade.Wrapf(cascade.KindInternal, err, "pews: creating temp file in %q", dir)
	}
	tmpPath := tmp.Name()
	if werr := writeAndSync(tmp, data); werr != nil {
		_ = os.Remove(tmpPath)
		return werr
	}
	if rerr := os.Rename(tmpPath, absPath); rerr != nil {
		_ = os.Remove(tmpPath)
		return cascade.Wrapf(cascade.KindInternal, rerr, "pews: renaming %q to %q", tmpPath, absPath)
	}
	return nil
}

// writeAndSync writes, syncs, and closes tmp; the caller removes tmp on
// error.
func writeAndSync(tmp *os.File, data []byte) error {
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return cascade.Wrapf(cascade.KindInternal, err, "pews: writing temp file %q", tmp.Name())
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return cascade.Wrapf(cascade.KindInternal, err, "pews: syncing temp file %q", tmp.Name())
	}
	if err := tmp.Close(); err != nil {
		return cascade.Wrapf(cascade.KindInternal, err, "pews: closing temp file %q", tmp.Name())
	}
	return nil
}
