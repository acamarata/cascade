// Purpose: the account copy set. DaemonCopies names every copy of one
// account's harness credential entry the daemon reads; ReadCopies reads
// exactly those (only not-found is absence); WriteDurable fans a new entry
// out to every differing copy, verifies each by hash, and on any failure
// restores every copy that held a non-blank entry. SPORT: HarnessCredentials/CHANGED.

package secrets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// CopyRef names one copy of an account's entry. Label is client, realpath
// or default; String and GoString print the label only, never a path.
type CopyRef struct {
	Label   string
	service string
	path    string
}

// String prints the label only.
func (r CopyRef) String() string { return r.Label }

// GoString prints the label only.
func (r CopyRef) GoString() string { return r.Label }

// CopyEntry is one copy as read: its entry, or Absent when not found.
type CopyEntry struct {
	Label  string
	Entry  Entry
	Absent bool
}

// DaemonCopies names every copy the daemon reads for the account whose
// harness config dir is configDir, on this platform.
func DaemonCopies(configDir string, resolve func(string) (string, error), defaultDir string) ([]CopyRef, error) {
	return daemonCopiesFor(goruntime.GOOS, configDir, resolve, defaultDir)
}

// daemonCopiesFor is DaemonCopies for one goos. darwin: the client service
// for the literal dir, the realpath service when it differs, and the
// unsuffixed default service only for the default dir. linux: the one file.
func daemonCopiesFor(goos, configDir string, resolve func(string) (string, error), defaultDir string) ([]CopyRef, error) {
	if !filepath.IsAbs(configDir) {
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: a harness config dir must be an absolute path")
	}
	dir := filepath.Clean(configDir)
	switch goos {
	case "darwin":
		refs := []CopyRef{{Label: "client", service: harnessServiceName(dir)}}
		resolved, err := resolve(dir)
		if err != nil {
			return nil, cascade.New(cascade.KindUnavailable, "secrets: the harness config dir could not be resolved")
		}
		if resolved = filepath.Clean(resolved); resolved != dir {
			refs = append(refs, CopyRef{Label: "realpath", service: harnessServiceName(resolved)})
		}
		if defaultDir != "" && dir == filepath.Clean(defaultDir) {
			refs = append(refs, CopyRef{Label: "default", service: strings.TrimSuffix(vendorKeychainServicePrefix, "-")})
		}
		return refs, nil
	case "linux":
		return []CopyRef{{Label: "client", path: filepath.Join(dir, ".credentials.json")}}, nil
	}
	return nil, errHarnessCredentialsUnsupported
}

// copyError names the copy's label and keeps the cause's kind; store errors
// never carry entry bytes.
func copyError(ref CopyRef, err error) error {
	kind, ok := cascade.KindOf(err)
	if !ok {
		kind = cascade.KindUnavailable
	}
	return cascade.Wrapf(kind, err, "secrets: harness credential copy %q", ref.Label)
}

// ReadCopies implements HarnessCredentials, in DaemonCopies order.
func (h *harnessCredentials) ReadCopies(ctx context.Context, configDir string) ([]CopyEntry, error) {
	refs, err := h.store.copyRefs(configDir)
	if err != nil {
		return nil, err
	}
	out := make([]CopyEntry, 0, len(refs))
	for _, ref := range refs {
		data, err := h.store.readCopy(ctx, ref)
		if cascade.HasKind(err, cascade.KindNotFound) {
			out = append(out, CopyEntry{Label: ref.Label, Absent: true})
			continue
		}
		if err == nil && !json.Valid(data) {
			err = cascade.New(cascade.KindIntegrity, "secrets: the entry is not a JSON document")
		}
		if err != nil {
			return nil, copyError(ref, err)
		}
		out = append(out, CopyEntry{Label: ref.Label, Entry: Entry{data: data}})
	}
	return out, nil
}

// blankEntry: absent, or a document holding no credential.
func blankEntry(data []byte) bool {
	switch string(bytes.TrimSpace(data)) {
	case "", "{}", "null":
		return true
	}
	return false
}

// copyRollbackSlot names a copy's rollback slot; the client copy keeps the
// single-copy name.
func copyRollbackSlot(configDir, label string) string {
	_, rollback := slotNames(configDir)
	if label == "client" {
		return rollback
	}
	return strings.TrimSuffix(rollback, ".rollback") + "." + label + ".rollback"
}

// WriteDurable implements HarnessCredentials: read every copy, stage e,
// keep a rollback of each differing non-blank copy, write and verify each
// differing copy, and on any failure restore every non-blank prior.
func (h *harnessCredentials) WriteDurable(ctx context.Context, configDir string, e Entry) error {
	if !json.Valid(e.data) {
		return cascade.New(cascade.KindInvalidInput, "secrets: refusing to write an entry that is not a JSON document")
	}
	refs, err := h.store.copyRefs(configDir)
	if err != nil {
		return err
	}
	priors, err := h.readPriors(ctx, refs)
	if err != nil {
		return err
	}
	staged, _ := slotNames(configDir)
	if err := h.stage(ctx, staged, e.data); err != nil {
		return err
	}
	var differ []int
	for i := range refs {
		if !bytes.Equal(priors[i], e.data) {
			differ = append(differ, i)
		}
	}
	if err := h.keepRollbacks(ctx, configDir, refs, priors, differ); err != nil {
		_ = h.custody.Delete(ctx, staged)
		return err
	}
	if err := h.writeCopies(ctx, refs, differ, e.data); err != nil {
		for _, i := range differ {
			if !blankEntry(priors[i]) {
				_ = h.store.writeCopy(ctx, refs[i], priors[i])
			}
		}
		_ = h.custody.Delete(ctx, staged)
		return err
	}
	_ = h.custody.Delete(ctx, staged)
	return nil
}

// readPriors reads every copy before any write: nil for a not-found copy;
// any other failure is returned as the store reported it.
func (h *harnessCredentials) readPriors(ctx context.Context, refs []CopyRef) ([][]byte, error) {
	priors := make([][]byte, len(refs))
	for i, ref := range refs {
		data, err := h.store.readCopy(ctx, ref)
		if cascade.HasKind(err, cascade.KindNotFound) {
			continue
		} else if err != nil {
			return nil, err
		}
		priors[i] = data
	}
	return priors, nil
}

// stage sets the staging slot once and verifies it reads back by hash.
func (h *harnessCredentials) stage(ctx context.Context, staged string, data []byte) error {
	if err := h.custody.Set(ctx, staged, data); err != nil {
		return err
	}
	got, err := h.custody.Get(ctx, staged)
	if err == nil && sha256.Sum256(got) != sha256.Sum256(data) {
		err = cascade.New(cascade.KindIntegrity, "secrets: the staged harness credential entry did not read back as written")
	}
	if err != nil {
		_ = h.custody.Delete(ctx, staged)
	}
	return err
}

// keepRollbacks stores each differing non-blank prior behind its expiry.
func (h *harnessCredentials) keepRollbacks(ctx context.Context, configDir string, refs []CopyRef, priors [][]byte, differ []int) error {
	expires := binary.BigEndian.AppendUint64(nil, uint64(h.clock.Now().Add(RollbackWindow).Unix()))
	for _, i := range differ {
		if blankEntry(priors[i]) {
			continue
		}
		slot := append(bytes.Clone(expires), priors[i]...)
		if err := h.custody.Set(ctx, copyRollbackSlot(configDir, refs[i].Label), slot); err != nil {
			return err
		}
	}
	return nil
}

// writeCopies writes each differing copy and verifies it by read-back hash.
func (h *harnessCredentials) writeCopies(ctx context.Context, refs []CopyRef, differ []int, data []byte) error {
	for _, i := range differ {
		if err := h.store.writeCopy(ctx, refs[i], data); err != nil {
			return copyError(refs[i], err)
		}
		if got, err := h.store.readCopy(ctx, refs[i]); err != nil || sha256.Sum256(got) != sha256.Sum256(data) {
			return cascade.Newf(cascade.KindIntegrity, "secrets: harness credential copy %q did not read back as written", refs[i].Label)
		}
	}
	return nil
}
