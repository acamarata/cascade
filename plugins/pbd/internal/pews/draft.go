// Package pews (draft.go): Purpose: draft-phase isolation (R-21.276) — the
// phase-record read (loadPhaseRecord/DecodePhase), the draft/active branch
// Store.LoadWithOptions implements over store.go's loader, the lenient
// ticket decoder a draft phase's tickets use in place of DecodeTicket, the
// authoring-side lint filter that drops the weight/cr_level floor rule for
// a draft candidate tree, and the typed build refusal RequireBuildable
// returns for a draft tree. No directory convention or marker file carries
// draft-ness anywhere in this file: Phase.Draft (schema.go) is the only
// representation, matching R-21.276 exactly.
//
// Inputs: a *Store/root directory (loadPhaseRecord), raw YAML bytes
// (DecodeDraftTicket, DecodePhase), or an already-loaded *Tree/LintReport
// (RequireBuildable, filterDraftLintIssues). No clock, no network.
// Outputs: a *Tree whose Draft field and Tickets/Tombstones reflect the
// draft/active boundary; *cascade.Error otherwise.
// Constraints: tests root every tree at t.TempDir() (Art.7); no bare
// time.Now; a draft phase's tickets never reach an active Tree unless the
// caller explicitly asks via LoadOptions.IncludeDrafts.
// SPORT: plugins/pbd/internal/pews draft-isolation (ADD) — P1-E14-W3-S29-T2.
package pews

import (
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/acamarata/cascade/pkg/cascade"
)

// phaseRecordFile is the phase-level record's filename at a tree's Root,
// sibling to tombstones.yaml.
const phaseRecordFile = "phase.yaml"

// ticketDecoder is the shape both DecodeTicket and DecodeDraftTicket share;
// store.go's loader is parameterized over it so the same walk serves both
// an active and a draft phase.
type ticketDecoder func(data []byte) (Ticket, error)

// LoadOptions controls Store.LoadWithOptions' draft-phase inclusion.
type LoadOptions struct {
	// IncludeDrafts, when true, is the only way a draft phase's tickets
	// enter the returned Tree (R-21.276). False (the default, and what
	// Load() always passes) excludes them: LoadWithOptions returns an
	// empty, non-error Tree without reading a single ticket file under a
	// draft phase's root, so a malformed or incomplete draft ticket can
	// never break an active read.
	IncludeDrafts bool
}

// LoadWithOptions is Load with explicit draft-inclusion control. It reads
// the phase record first (loadPhaseRecord): when the phase is a draft and
// opts.IncludeDrafts is false, it returns &Tree{Phase: s.Phase, Draft:
// true} immediately, with Tickets and Tombstones both nil — the tree's
// ticket files are never opened, let alone decoded, for an excluded draft.
// Otherwise it loads exactly as Load always has, using DecodeDraftTicket
// (which skips weight/model_class validation, R-21.276) in place of
// DecodeTicket whenever the phase is a draft.
func (s *Store) LoadWithOptions(opts LoadOptions) (*Tree, error) {
	if s.Root == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "pews: store root must not be empty")
	}
	if s.Phase == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "pews: store phase must not be empty")
	}
	info, err := os.Stat(s.Root)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindNotFound, err, "pews: tree root %q", s.Root)
	}
	if !info.IsDir() {
		return nil, cascade.Newf(cascade.KindInvalidInput, "pews: tree root %q is not a directory", s.Root)
	}

	phaseRec, perr := loadPhaseRecord(s.Root)
	if perr != nil {
		return nil, perr
	}
	if phaseRec.Draft && !opts.IncludeDrafts {
		return &Tree{Phase: s.Phase, Draft: true}, nil
	}

	decode := ticketDecoder(DecodeTicket)
	if phaseRec.Draft {
		decode = DecodeDraftTicket
	}
	tickets, err := s.loadTickets(decode)
	if err != nil {
		return nil, err
	}
	tombstones, err := s.loadTombstones()
	if err != nil {
		return nil, err
	}
	sort.Slice(tickets, func(i, j int) bool { return tickets[i].CanonicalID < tickets[j].CanonicalID })
	return &Tree{Phase: s.Phase, Draft: phaseRec.Draft, Tickets: tickets, Tombstones: tombstones}, nil
}

// loadPhaseRecord reads root/phase.yaml. A missing file means the phase is
// not a draft — that is structurally valid, not an error, matching
// store.go's loadTombstones convention for its own optional root file.
func loadPhaseRecord(root string) (Phase, error) {
	path := filepath.Join(root, phaseRecordFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Phase{}, nil
		}
		return Phase{}, cascade.Wrapf(cascade.KindInternal, err, "pews: reading %q", path)
	}
	return DecodePhase(data)
}

// DecodePhase parses data as a PEWS phase record (schema.go's Phase). An
// unparseable document is a *cascade.Error of kind cascade.KindInvalidInput
// and a zero Phase.
func DecodePhase(data []byte) (Phase, error) {
	var p Phase
	if err := yaml.Unmarshal(data, &p); err != nil {
		return Phase{}, cascade.Wrap(cascade.KindInvalidInput, err, "pews: malformed phase YAML")
	}
	return p, nil
}

// DecodeDraftTicket parses data as a PEWS ticket-schema v2 document exactly
// as DecodeTicket does, except it skips the Weight/ModelClass validity
// checks DecodeTicket's validateEnums always runs (R-21.276: "a draft phase
// accepts ticket edits without weight/model validation"). CRLevel and
// QALevel are still checked — the ruling names only weight and model_class.
// Like DecodeTicket, it never panics and fails closed on every other axis
// (malformed YAML, non-mapping root, unknown/missing field, wrong-shaped
// value).
func DecodeDraftTicket(data []byte) (t Ticket, err error) {
	defer func() {
		if r := recover(); r != nil {
			t, err = Ticket{}, cascade.Newf(cascade.KindInvalidInput, "draft ticket YAML decode panicked: %v", r)
		}
	}()

	var doc yaml.Node
	if uerr := yaml.Unmarshal(data, &doc); uerr != nil {
		return Ticket{}, cascade.Wrap(cascade.KindInvalidInput, uerr, "malformed draft ticket YAML")
	}
	root, rerr := mappingRoot(&doc)
	if rerr != nil {
		return Ticket{}, rerr
	}
	if kerr := checkKeys(root); kerr != nil {
		return Ticket{}, kerr
	}

	var raw Ticket
	if derr := root.Decode(&raw); derr != nil {
		return Ticket{}, cascade.Wrap(cascade.KindInvalidInput, derr, "malformed draft ticket YAML value")
	}
	if !raw.CRLevel.Valid() {
		return Ticket{}, cascade.Newf(cascade.KindInvalidInput, "invalid cr_level %q", raw.CRLevel)
	}
	if !raw.QALevel.Valid() {
		return Ticket{}, cascade.Newf(cascade.KindInvalidInput, "invalid qa_level %q", raw.QALevel)
	}
	return raw, nil
}

// filterDraftLintIssues drops every LintKindCRWeightMismatch issue (the
// weight -> cr_level floor rule, lint_rules.go's ruleCRWeight) from issues.
// It is the second half of R-21.276's "without weight/model validation":
// DecodeDraftTicket covers decode-time enum validity, and this covers the
// one Lint rule that separately re-derives a weight-shaped constraint.
// Lint (lint.go) is outside this ticket's files_scope, so this filters its
// already-computed report rather than changing which rules Lint runs.
func filterDraftLintIssues(issues []LintIssue) []LintIssue {
	var out []LintIssue
	for _, i := range issues {
		if i.Kind == LintKindCRWeightMismatch {
			continue
		}
		out = append(out, i)
	}
	return out
}

// RequireBuildable refuses tree when it is a draft: a draft phase cannot be
// built (R-21.276). The returned error is cascade.KindConflict — the
// phase's own recorded state (draft) conflicts with the build precondition
// — never a bare error a caller would have to string-match. A nil tree is
// itself refused as invalid input, matching Validate's own nil-tree
// handling. This is the typed refusal only: no dispatch/build machinery
// exists in this ticket's scope (N/S-30 owns it) for this to gate yet.
func RequireBuildable(tree *Tree) error {
	if tree == nil {
		return cascade.New(cascade.KindInvalidInput, "pews: cannot check buildability of a nil tree")
	}
	if tree.Draft {
		return cascade.Newf(cascade.KindConflict, "pews: phase %q is a draft phase and cannot be built", tree.Phase)
	}
	return nil
}

// inSet reports whether v appears in set. It backs every closed-set
// membership check in schema.go, both for the string field names checkKeys
// validates and for the typed enum values Weight/ModelClass/QALevel.Valid
// check. Declared here rather than schema.go purely for that file's own
// 300-line budget (Art.10.5) — this ticket's Phase addition needed the
// room; inSet itself carries no draft-isolation behavior.
func inSet[T comparable](v T, set []T) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}
