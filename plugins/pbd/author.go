// Package pbd (author.go): Purpose: the orchestration layer between the
// mounted `pbd create|edit|move` commands (pbd.go) and the native
// authoring engine in internal/pews — decodes a caller-supplied ticket
// file (create, edit) or a pair of ids (move), and calls straight into
// pews.Create/Edit/Move, which pre-flights every write through the same
// Lint (composing Validate) `pbd validate`/`pbd lint` run before a byte
// touches disk.
// Inputs: RunCommand's args, decoded per-verb by runCreateCommand/
// runEditCommand/runMoveCommand (pbd.go dispatches these); RunCreate/
// RunEdit/RunMove take already-parsed arguments for direct callers (a
// test harness, or a later command sharing this engine).
// Outputs: nil on success; every failure is the *cascade.Error
// pews.Create/Edit/Move (or a file-read/decode step here) returned.
// Constraints: imports pkg/** and internal/pews ONLY (Art.10.2) — no
// internal/output; stdlib os for reading the caller-supplied ticket file.
// SPORT: plugins/pbd author (ADD) — P1-E14-W3-S28-T3.
package pbd

import (
	"os"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
)

// createCommandName, editCommandName, and moveCommandName are the three
// PEWS authoring verbs T3 mounts beneath the pbd namespace, alongside T2's
// validate and T4's lint.
const (
	createCommandName = "create"
	editCommandName   = "edit"
	moveCommandName   = "move"
)

// RunCreate reads the ticket-schema v2 YAML at ticketFile and authors it
// at the canonical tree position its own id implies, rooted at root
// (ticket ids named under phase, or DefaultPhase when phase is empty).
func RunCreate(root, ticketFile, phase string) error {
	t, err := decodeTicketFile(ticketFile)
	if err != nil {
		return err
	}
	return pews.Create(root, resolvePhase(phase), t)
}

// RunEdit reads the ticket-schema v2 YAML at ticketFile and overwrites the
// existing ticket at ticketID's canonical position with it. It refuses
// when the file's own id does not match ticketID, before ever touching
// the tree.
func RunEdit(root, ticketID, ticketFile, phase string) error {
	t, err := decodeTicketFile(ticketFile)
	if err != nil {
		return err
	}
	if t.ID != ticketID {
		return cascade.Newf(cascade.KindInvalidInput,
			"pbd edit: ticket file declares id %q, does not match target id %q", t.ID, ticketID)
	}
	return pews.Edit(root, resolvePhase(phase), t)
}

// RunMove relocates the ticket at fromID's canonical position to toID's.
func RunMove(root, fromID, toID, phase string) error {
	return pews.Move(root, resolvePhase(phase), fromID, toID)
}

// resolvePhase returns phase, or DefaultPhase when phase is empty.
func resolvePhase(phase string) string {
	if phase == "" {
		return DefaultPhase
	}
	return phase
}

// decodeTicketFile reads and decodes the ticket-schema v2 YAML at path.
func decodeTicketFile(path string) (pews.Ticket, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return pews.Ticket{}, cascade.Wrapf(cascade.KindNotFound, err, "pbd: reading ticket file %q", path)
	}
	t, derr := pews.DecodeTicket(data)
	if derr != nil {
		return pews.Ticket{}, cascade.Wrapf(cascade.KindInvalidInput, derr, "pbd: decoding ticket file %q", path)
	}
	return t, nil
}

// runCreateCommand parses RunCommand's args for `pbd create <root>
// <ticket-file> [phase]`.
func runCreateCommand(args []string) error {
	if len(args) < 2 || args[0] == "" || args[1] == "" {
		return cascade.New(cascade.KindInvalidInput, "pbd create: usage: pbd create <root> <ticket-file> [phase]")
	}
	return RunCreate(args[0], args[1], optionalArg(args, 2))
}

// runEditCommand parses RunCommand's args for `pbd edit <root>
// <ticket-id> <ticket-file> [phase]`.
func runEditCommand(args []string) error {
	if len(args) < 3 || args[0] == "" || args[1] == "" || args[2] == "" {
		return cascade.New(cascade.KindInvalidInput, "pbd edit: usage: pbd edit <root> <ticket-id> <ticket-file> [phase]")
	}
	return RunEdit(args[0], args[1], args[2], optionalArg(args, 3))
}

// runMoveCommand parses RunCommand's args for `pbd move <root> <from-id>
// <to-id> [phase]`.
func runMoveCommand(args []string) error {
	if len(args) < 3 || args[0] == "" || args[1] == "" || args[2] == "" {
		return cascade.New(cascade.KindInvalidInput, "pbd move: usage: pbd move <root> <from-id> <to-id> [phase]")
	}
	return RunMove(args[0], args[1], args[2], optionalArg(args, 3))
}

// optionalArg returns args[i], or "" when args is too short.
func optionalArg(args []string, i int) string {
	if len(args) > i {
		return args[i]
	}
	return ""
}
