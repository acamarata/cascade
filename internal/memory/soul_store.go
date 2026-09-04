package memory

// Purpose: FileSoulStore, the audited SOUL store, and the on-disk ledger
//   that carries its version counter and its audit log. Every one of the
//   three sanctioned routes funnels through the single applyEdit path
//   here, so there is exactly one place where the system's model of the
//   user can change, and it always increments the version and appends an
//   entry.
// Inputs: a base directory, an injected Clock, an optional divergence
//   sink, and caller documents.
// Outputs: SOUL.md and soul-ledger.json under {base}/soul, typed views,
//   and pkg/cascade taxonomy errors.
// Constraints: no bare time.Now; the document is written BEFORE the
//   ledger (see applyEdit) so an interruption can never leave a version
//   claiming content that was never written; the ledger fails closed on a
//   damaged or unknown-version file.
// SPORT: G/memory-soul-store (ADD, placeholder per T-2 sport_updates).

import (
	"context"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// CONTRACT DEVIATION (recorded, not papered over). The contract specifies
// "a version-and-audit ledger in the internal/storage B-layer". The ledger
// is kept as a file beside the document instead, for a reason the
// contract's own route (b) creates: the SOUL BODY must be a plain file a
// user can edit with any editor, and a version counter living in SQLite
// while the content lives in a file gives two stores with no atomicity
// between them. There is no ordering of those two writes that an
// interruption cannot leave inconsistent, and the inconsistent state is
// the dangerous one — a version and an audit entry describing content
// that was never written is a confident, wrong model of the user. With
// both halves in one directory the ordering below chooses which side an
// interruption falls on: the document lands first, so a crash leaves real
// content on disk and a ledger that has not claimed it, which route (b)
// reports as a divergence rather than absorbing. Nothing is lost and
// nothing is claimed that is not on disk. See the journal for both sides.

// soulFormatVersion is the ledger format this build writes. A ledger
// declaring any other version is refused with ErrUnsupportedSoulFormat
// rather than read on a best-effort basis: guessing at a layout written by
// a build that knew more than this one is how a version counter silently
// resets and an audit log silently loses entries.
const soulFormatVersion = 1

// The SOUL's two files. The document is markdown with no header of any
// kind, because it is the one file in this package a person is expected to
// open and edit; every machine-managed field lives in the ledger beside
// it, out of the user's way.
const (
	soulDir          = "soul"
	soulDocumentFile = "SOUL.md"
	soulLedgerFile   = "soul-ledger.json"
)

// FileSoulStore is the SOUL store: one document file and one ledger file
// under {base}/soul.
//
// The SOUL is the system's model of the person it serves, so this type is
// arranged around one idea: there is a single write path. Edit,
// EditViaChat and the route-(b) reconcile all call applyEdit, which is the
// only function in the package that increments the version or appends to
// the audit log. A fourth way to change the document would be a way for
// the system's beliefs about someone to change with no record of it.
type FileSoulStore struct {
	base  string
	clock Clock
	fs    fileSystem
	sink  SoulDivergenceSink
}

// Compile-time proof that the implementation satisfies the contract, so a
// drifting method set fails the build rather than a caller.
var _ SoulStore = (*FileSoulStore)(nil)

// NewFileSoulStore returns a SOUL store rooted at base, taking its
// timestamps from clk. Conflicts are reported to sink; a nil sink discards
// them and changes nothing that is stored.
func NewFileSoulStore(base string, clk Clock, sink SoulDivergenceSink) *FileSoulStore {
	return newSoulStoreWithFS(base, clk, sink, osFS{})
}

// newSoulStoreWithFS is NewFileSoulStore with the file-system seam
// supplied. Unexported: tests inject a failing file system through it, and
// no shipped path may substitute anything for osFS.
func newSoulStoreWithFS(
	base string, clk Clock, sink SoulDivergenceSink, sys fileSystem,
) *FileSoulStore {
	if sink == nil {
		sink = discardSoulEvents{}
	}
	return &FileSoulStore{base: base, clock: clk, fs: sys, sink: sink}
}

// documentPath is the file the user edits.
func (s *FileSoulStore) documentPath() string {
	return filepath.Join(s.base, soulDir, soulDocumentFile)
}

// ledgerPath is the machine-managed half.
func (s *FileSoulStore) ledgerPath() string {
	return filepath.Join(s.base, soulDir, soulLedgerFile)
}

// readDocument returns the body on disk. found is false only when no file
// is there; an unreadable file is an error, never a silent absence,
// because an empty SOUL read from a damaged file is a wrong model of the
// user rather than a missing one.
func (s *FileSoulStore) readDocument() (string, bool, error) {
	data, err := s.fs.ReadFile(s.documentPath())
	if err != nil {
		if isNotExist(err) {
			return "", false, nil
		}
		return "", false, cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"reading soul document: %v", err)
	}
	return string(data), true, nil
}

// applyEdit is the ONE write path. Every route calls it, and it is the
// only function that moves the version counter or appends an audit entry.
//
// # Write order
//
// The document is written FIRST and the ledger second. The reverse order
// would leave a ledger claiming a version and an audit entry for content
// that is not on disk: a confident, WRONG model of the user, which is the
// failure this whole store is arranged to avoid. With this order an
// interruption leaves the document on disk and the ledger behind it, which
// the next load sees as a divergence and reports — the content survives,
// nothing is claimed that was not written, and a person decides what the
// document should say.
func (s *FileSoulStore) applyEdit(
	ctx context.Context, doc SoulDocument, route SoulEditRoute,
) (SoulView, error) {
	if err := ctx.Err(); err != nil {
		return SoulView{}, cascade.Wrap(cascade.KindCanceled, err, "soul edit canceled")
	}
	if !route.Valid() {
		return SoulView{}, cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidSoulRoute,
			"unknown soul edit route %q", string(route))
	}
	doc = doc.canonical()
	if err := doc.Validate(); err != nil {
		return SoulView{}, err
	}
	led, err := s.loadLedger()
	if err != nil {
		return SoulView{}, err
	}
	if err := s.fs.WriteAtomic(s.documentPath(), []byte(doc.Body)); err != nil {
		return SoulView{}, cascade.Wrapf(cascade.KindUnavailable, ErrStoreIO,
			"writing soul document: %v", err)
	}
	led = s.advance(led, doc, route)
	if err := s.persistLedger(led); err != nil {
		return SoulView{}, err
	}
	return SoulView{Document: doc, Version: led.Version}, nil
}

// advance folds one accepted edit into the ledger: version plus one,
// exactly one appended entry, and the new digest.
//
// LastReconciledVersion moves only for the reconcile route, which is what
// the field means: the version as of the last time the store adopted the
// FILE's own content. A store-side write leaves it behind on purpose, so a
// later out-of-store edit that may not have seen this write is reported as
// a conflict rather than adopted over it.
func (s *FileSoulStore) advance(led soulLedger, doc SoulDocument, route SoulEditRoute) soulLedger {
	now := s.clock.Now().UTC()
	next := HashBody(doc.Body)
	led.Format = soulFormatVersion
	led.Version++
	led.Schema = doc.Schema
	led.UpdatedAt = now
	led.Entries = append(led.Entries, AuditEntry{
		Version:   led.Version,
		Route:     route,
		EditedAt:  now,
		DeltaHash: HashBody(led.ContentHash + ":" + next),
	})
	led.ContentHash = next
	if route == SoulRouteFileReconcile {
		led.LastReconciledVersion = led.Version
	}
	return led
}

// Edit applies doc through route (a), the CLI verb.
//
// It reconciles first. Writing over a file the user has edited outside the
// store, without adopting that edit, would destroy the user's own words
// with no record that they ever existed; reconciling first means that edit
// becomes version N and the caller's document becomes version N+1, and the
// log shows both.
func (s *FileSoulStore) Edit(ctx context.Context, doc SoulDocument) (SoulView, error) {
	if err := s.reconcileBeforeWrite(ctx); err != nil {
		return SoulView{}, err
	}
	return s.applyEdit(ctx, doc, SoulRouteCLI)
}

// reconcileBeforeWrite runs route (b) ahead of an explicit write and
// decides what an unresolved conflict means for that write.
//
// A conflict does NOT block the write, and that is deliberate. Route (b)
// refuses to resolve a conflict on its own — it emits the event, leaves
// the note, and touches nothing — but a caller reaching for Edit or
// EditViaChat is a person or a chat turn stating what the document should
// now say. Refusing them too would leave a diverged SOUL with no route
// back to agreement at all, since every write path would refuse forever.
// So the conflict is recorded (event, note, and the audit entry the write
// itself appends) and the explicit instruction is honoured. Nothing here
// merges anything, and nothing is discarded silently: the divergence is on
// the bus, in the note, and in the log before the write lands.
func (s *FileSoulStore) reconcileBeforeWrite(ctx context.Context) error {
	if _, err := s.DetectDivergence(ctx); err != nil && !isSoulConflict(err) {
		return err
	}
	return nil
}

// EditViaChat applies doc through route (c), the chat-mediated API.
//
// It is a real method with a real implementation, not a placeholder for
// the chat surface that will call it: it reconciles and then applies
// through the same audited path as every other route, so a chat-mediated
// change increments the version and appends an entry exactly as a CLI one
// does. The command surface that mounts it is a later ticket's work; the
// behaviour it mounts is here and tested.
func (s *FileSoulStore) EditViaChat(ctx context.Context, doc SoulDocument) error {
	if err := s.reconcileBeforeWrite(ctx); err != nil {
		return err
	}
	_, err := s.applyEdit(ctx, doc, SoulRouteChat)
	return err
}

// Get returns the current SOUL, reconciling an out-of-store edit on the
// way (route b). A conflict is reported through SoulView.Diverged rather
// than raised: a reader is told the document may be stale, and is not
// denied the document the store does hold.
func (s *FileSoulStore) Get(ctx context.Context) (SoulView, error) {
	res, err := s.DetectDivergence(ctx)
	if err != nil && !isSoulConflict(err) {
		return SoulView{}, err
	}
	led, lerr := s.loadLedger()
	if lerr != nil {
		return SoulView{}, lerr
	}
	body, found, rerr := s.readDocument()
	if rerr != nil {
		return SoulView{}, rerr
	}
	if !found || led.Version == 0 {
		return SoulView{}, cascade.Wrapf(cascade.KindNotFound, ErrNoSoulDocument,
			"no soul document has been written")
	}
	return SoulView{
		Document: led.document(body),
		Version:  led.Version,
		Diverged: res.Outcome == DivergenceConflict,
	}, nil
}

// Export serialises the current document and the whole audit log.
//
// What it returns is the whole of what an export contains: this document,
// this log, the envelope version and the instant. No other memory record,
// no file path, no machine or environment detail is read here or reachable
// from what is returned — see SoulExport's own doc comment for why that
// list is the type's contract and not an implementation detail.
func (s *FileSoulStore) Export(ctx context.Context) (SoulExport, error) {
	view, err := s.Get(ctx)
	if err != nil {
		return SoulExport{}, err
	}
	led, err := s.loadLedger()
	if err != nil {
		return SoulExport{}, err
	}
	entries := make([]AuditEntry, len(led.Entries))
	for i, e := range led.Entries {
		e.EditedAt = e.EditedAt.UTC()
		entries[i] = e
	}
	return SoulExport{
		SchemaVersion: SoulSchemaVersion,
		ExportedAt:    s.clock.Now().UTC(),
		Soul:          view.Document,
		AuditEntries:  entries,
	}, nil
}
