// Package pews (store.go): Purpose: the filesystem-backed loader for the
//
//	canonical PEWS tree (phase/epics/E-*/waves/W-*/sprints/S-*/tickets/
//	T-*.yaml), decoding every ticket file through DecodeTicket (schema.go)
//	and assembling the in-memory Tree the validator (validate.go)
//	inspects.
//
// Inputs: a root directory and a phase id, both caller-provided; no
//
//	clock, no network, no environment reads beyond the filesystem calls
//	this file makes directly against Root.
//
// Outputs: *Tree (deterministically ordered TicketRecords, plus any
//
//	recorded Tombstones); every failure is a *cascade.Error.
//
// Constraints: tests root every tree at t.TempDir() (Art.7); no bare
//
//	time.Now; Load never returns a partially populated Tree on error — a
//	failed Load always returns (nil, err).
//
// SPORT: plugins/pbd/internal/pews store (ADD) — P1-E14-W3-S28-T2.
package pews

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/acamarata/cascade/pkg/cascade"
)

var (
	epicDirRe    = regexp.MustCompile(`^E-([A-Z]+)$`)
	waveDirRe    = regexp.MustCompile(`^W-([0-9]+)$`)
	sprintDirRe  = regexp.MustCompile(`^S-([0-9]+)$`)
	ticketFileRe = regexp.MustCompile(`^T-([0-9]+)\.yaml$`)
)

// TicketRecord is one loaded ticket, decoded and located at its canonical
// tree position.
type TicketRecord struct {
	// ID is the ticket's own declared id field, exactly as decoded — not
	// necessarily equal to CanonicalID; validate.go reports any mismatch
	// as a violation rather than silently correcting it.
	ID string
	// CanonicalID is the id the tree POSITION implies: phase, epic
	// (letters converted to their bijective base-26 ordinal), wave,
	// sprint, and ticket number.
	CanonicalID string
	// Ticket is the decoded ticket contract.
	Ticket Ticket
	// RelPath is the ticket file's path relative to the store's Root.
	RelPath     string
	EpicLetters string
	EpicNum     int
	Wave        int
	Sprint      int
	TicketNum   int
}

// Tombstone is one retired-ticket-id bookkeeping entry, read from the
// tree's optional root-level tombstones.yaml.
type Tombstone struct {
	// ID is the retired ticket's canonical id.
	ID string `yaml:"id"`
	// Reason is why the id was retired.
	Reason string `yaml:"reason"`
}

// Tree is a fully loaded, in-memory PEWS tree: every ticket found, plus
// the recorded tombstones.
type Tree struct {
	Phase      string
	Tickets    []TicketRecord
	Tombstones []Tombstone
}

// Store loads a PEWS tree rooted at Root, naming its tickets under the
// Phase prefix (e.g. "P1").
type Store struct {
	Root  string
	Phase string
}

// NewStore constructs a Store for root/phase. Neither is validated until
// Load is called.
func NewStore(root, phase string) *Store {
	return &Store{Root: root, Phase: phase}
}

// Load walks the store's tree and decodes every ticket file it finds. It
// never returns a partially populated Tree: any structural or decode
// failure returns (nil, err) with err a *cascade.Error.
func (s *Store) Load() (*Tree, error) {
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

	tickets, err := s.loadTickets()
	if err != nil {
		return nil, err
	}
	tombstones, err := s.loadTombstones()
	if err != nil {
		return nil, err
	}
	sort.Slice(tickets, func(i, j int) bool { return tickets[i].CanonicalID < tickets[j].CanonicalID })
	return &Tree{Phase: s.Phase, Tickets: tickets, Tombstones: tombstones}, nil
}

// loadTickets walks Root/epics and decodes every ticket file beneath it.
func (s *Store) loadTickets() ([]TicketRecord, error) {
	epicsDir := filepath.Join(s.Root, "epics")
	epicEntries, err := readDirIfExists(epicsDir)
	if err != nil {
		return nil, err
	}
	var out []TicketRecord
	for _, ee := range epicEntries {
		if !ee.IsDir() {
			continue
		}
		m := epicDirRe.FindStringSubmatch(ee.Name())
		if m == nil {
			return nil, cascade.Newf(cascade.KindInvalidInput, "pews: malformed epic directory name %q", ee.Name())
		}
		letters := m[1]
		recs, err := s.loadWaves(filepath.Join(epicsDir, ee.Name()), letters)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

func (s *Store) loadWaves(epicDir, letters string) ([]TicketRecord, error) {
	waveEntries, err := readDirIfExists(filepath.Join(epicDir, "waves"))
	if err != nil {
		return nil, err
	}
	var out []TicketRecord
	for _, we := range waveEntries {
		if !we.IsDir() {
			continue
		}
		m := waveDirRe.FindStringSubmatch(we.Name())
		if m == nil {
			return nil, cascade.Newf(cascade.KindInvalidInput, "pews: malformed wave directory name %q", we.Name())
		}
		wave, _ := strconv.Atoi(m[1])
		recs, err := s.loadSprints(filepath.Join(epicDir, "waves", we.Name()), letters, wave)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

func (s *Store) loadSprints(waveDir, letters string, wave int) ([]TicketRecord, error) {
	sprintEntries, err := readDirIfExists(filepath.Join(waveDir, "sprints"))
	if err != nil {
		return nil, err
	}
	var out []TicketRecord
	for _, se := range sprintEntries {
		if !se.IsDir() {
			continue
		}
		m := sprintDirRe.FindStringSubmatch(se.Name())
		if m == nil {
			return nil, cascade.Newf(cascade.KindInvalidInput, "pews: malformed sprint directory name %q", se.Name())
		}
		sprint, _ := strconv.Atoi(m[1])
		recs, err := s.loadTicketFiles(filepath.Join(waveDir, "sprints", se.Name()), letters, wave, sprint)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
	}
	return out, nil
}

func (s *Store) loadTicketFiles(sprintDir, letters string, wave, sprint int) ([]TicketRecord, error) {
	ticketEntries, err := readDirIfExists(filepath.Join(sprintDir, "tickets"))
	if err != nil {
		return nil, err
	}
	epicNum := epicNumberFromLetters(letters)
	var out []TicketRecord
	for _, te := range ticketEntries {
		if te.IsDir() {
			continue
		}
		m := ticketFileRe.FindStringSubmatch(te.Name())
		if m == nil {
			return nil, cascade.Newf(cascade.KindInvalidInput, "pews: malformed ticket file name %q", te.Name())
		}
		ticketNum, _ := strconv.Atoi(m[1])
		path := filepath.Join(sprintDir, "tickets", te.Name())
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil, cascade.Wrapf(cascade.KindInternal, rerr, "pews: reading %q", path)
		}
		t, derr := DecodeTicket(data)
		if derr != nil {
			return nil, cascade.Wrapf(cascade.KindInvalidInput, derr, "pews: decoding %q", path)
		}
		rel, _ := filepath.Rel(s.Root, path)
		out = append(out, TicketRecord{
			ID:          t.ID,
			CanonicalID: canonicalID(s.Phase, epicNum, wave, sprint, ticketNum),
			Ticket:      t,
			RelPath:     rel,
			EpicLetters: letters,
			EpicNum:     epicNum,
			Wave:        wave,
			Sprint:      sprint,
			TicketNum:   ticketNum,
		})
	}
	return out, nil
}

// loadTombstones reads Root/tombstones.yaml. A missing file means the tree
// has no recorded tombstones yet — that is structurally valid, not an
// error.
func (s *Store) loadTombstones() ([]Tombstone, error) {
	path := filepath.Join(s.Root, "tombstones.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, cascade.Wrapf(cascade.KindInternal, err, "pews: reading %q", path)
	}
	var doc struct {
		Tombstones []Tombstone `yaml:"tombstones"`
	}
	if uerr := yaml.Unmarshal(data, &doc); uerr != nil {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, uerr, "pews: malformed tombstones YAML %q", path)
	}
	return doc.Tombstones, nil
}

// readDirIfExists reads dir's entries sorted by name, or returns (nil, nil)
// when dir does not exist — an empty tree, or a tree with no waves/sprints/
// tickets yet under some node, is structurally valid.
func readDirIfExists(dir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, cascade.Wrapf(cascade.KindInternal, err, "pews: reading directory %q", dir)
	}
	return entries, nil
}

// epicNumberFromLetters converts an epic's directory letters (A, B, ...,
// Z, AA, AB, ...) to its 1-based bijective base-26 ordinal — the same
// numbering the real PEWS tree's ids use (verified: E-N -> 14, E-AH -> 34,
// E-AR -> 44). letters is assumed already validated as [A-Z]+ by
// epicDirRe.
func epicNumberFromLetters(letters string) int {
	n := 0
	for i := 0; i < len(letters); i++ {
		n = n*26 + int(letters[i]-'A'+1)
	}
	return n
}

// canonicalID formats the id a ticket at (epicNum, wave, sprint,
// ticketNum) must declare: "<phase>-E<epicNum>-W<wave>-S<sprint>-T<n>".
// Epic and sprint are zero-padded to a minimum of two digits (matching the
// real tree's own directory widths); wave and ticket number are never
// padded.
func canonicalID(phase string, epicNum, wave, sprint, ticketNum int) string {
	return fmt.Sprintf("%s-E%02d-W%d-S%02d-T%d", phase, epicNum, wave, sprint, ticketNum)
}
