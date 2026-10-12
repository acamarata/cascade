package plugin

// Purpose: registry index rollback and replay refusal for RegistryClient.
//   After an index's signature verifies, the client compares its signed
//   Sequence with the last one it accepted for the same registry URL and
//   refuses an older index, the same sequence with different bytes, or an
//   unsequenced (sequence 0) index. The accepted sequence and the sha256 of
//   the verified bytes are stored in <CacheDir>/index.seq as
//   {"<registry url>": {"sequence": N, "sha256": "<hex>"}}.
// Inputs: a verified RegistryIndex and the exact bytes that verified.
// Outputs: nil when the index may be used (the baseline is moved forward),
//   or a KindIntegrity *cascade.Error whose message starts with
//   "registry index rollback".
// Constraints: callers pass only verified indexes, so index.seq is read and
//   written only after signature verification and an unverified index
//   never moves the baseline. A refusal leaves index.seq byte-identical.
//   The write is atomic (temp file in the same directory, fsync, rename);
//   it mirrors internal/runtime.WriteFileAtomic because pkg/ never imports
//   internal/ (Art.10.2). A missing index.seq is a first fetch and accepts;
//   an unreadable one refuses (fail closed). With no CacheDir the baseline
//   lives in memory for the client's lifetime; the production composition
//   root always sets CacheDir. Residuals: two processes sharing CacheDir
//   are not locked against each other (seqMu is per client), so a slower
//   writer can lower a baseline another process just raised; a same-uid process
//   can delete index.seq; an attacker can replay the latest valid index
//   (freeze; the 90-day published_at warning in Fetch narrows it); the
//   digest covers the fetched bytes (per the contract), so a re-serialised
//   copy of the current signed index is refused until the next sequence
//   (availability only, never acceptance of unsigned content).
// SPORT: pkg/plugin registry-index-rollback (ADD).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	// registrySeqFile is the baseline file's name inside CacheDir.
	registrySeqFile = "index.seq"
	// registryRollbackMsg prefixes every rollback refusal.
	registryRollbackMsg = "registry index rollback"
	// registryStaleAfter is the published_at age past which Fetch warns.
	registryStaleAfter = 90 * 24 * time.Hour
)

// seqBaseline is one registry URL's last accepted index.
type seqBaseline struct {
	Sequence uint64 `json:"sequence"`
	SHA256   string `json:"sha256"`
}

// acceptSequence refuses idx when it would roll the registry back and
// otherwise records it as the new baseline. verifiedRaw must be the exact
// bytes whose signature produced idx.
func (c *RegistryClient) acceptSequence(idx RegistryIndex, verifiedRaw []byte) error {
	if idx.Sequence == 0 {
		return cascade.New(cascade.KindIntegrity, registryRollbackMsg+": index carries no sequence (sequence 0 is never accepted)")
	}
	c.seqMu.Lock()
	defer c.seqMu.Unlock()
	all, err := c.readBaselines()
	if err != nil {
		return err
	}
	sum := sha256.Sum256(verifiedRaw)
	digest := hex.EncodeToString(sum[:])
	if prev, ok := all[c.cfg.RegistryURL]; ok {
		if idx.Sequence < prev.Sequence {
			return cascade.Newf(cascade.KindIntegrity, "%s: sequence %d is older than the accepted sequence %d", registryRollbackMsg, idx.Sequence, prev.Sequence)
		}
		if idx.Sequence == prev.Sequence && digest != prev.SHA256 {
			return cascade.Newf(cascade.KindIntegrity, "%s: sequence %d was already accepted with different bytes", registryRollbackMsg, idx.Sequence)
		}
		if idx.Sequence == prev.Sequence {
			return nil
		}
	}
	all[c.cfg.RegistryURL] = seqBaseline{Sequence: idx.Sequence, SHA256: digest}
	return c.writeBaselines(all)
}

// readBaselines returns every stored baseline. A missing file is an empty
// map (first fetch); an unreadable or corrupt one refuses.
func (c *RegistryClient) readBaselines() (map[string]seqBaseline, error) {
	if c.cfg.CacheDir == "" {
		if c.memBaseline == nil {
			c.memBaseline = map[string]seqBaseline{}
		}
		return c.memBaseline, nil
	}
	path := filepath.Join(c.cfg.CacheDir, registrySeqFile)
	data, err := os.ReadFile(path) //nolint:gosec // path is CacheDir (operator config) plus a fixed name
	if os.IsNotExist(err) {
		return map[string]seqBaseline{}, nil
	}
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindIntegrity, err, "%s: read baseline %s", registryRollbackMsg, path)
	}
	all := map[string]seqBaseline{}
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, cascade.Wrapf(cascade.KindIntegrity, err, "%s: baseline %s is corrupt (delete it to accept the next verified index as a first fetch)", registryRollbackMsg, path)
	}
	return all, nil
}

// writeBaselines stores all atomically under CacheDir (or in memory when
// there is no CacheDir).
func (c *RegistryClient) writeBaselines(all map[string]seqBaseline) error {
	if c.cfg.CacheDir == "" {
		c.memBaseline = all
		return nil
	}
	data, err := json.Marshal(all)
	if err != nil {
		return cascade.Wrapf(cascade.KindInternal, err, "%s: encode baseline", registryRollbackMsg)
	}
	return writeFileAtomic(filepath.Join(c.cfg.CacheDir, registrySeqFile), data)
}

// writeFileAtomic writes data to path with mode 0600 through a temp file in
// the same directory, fsync and rename, so readers see the old or the new
// content, never a torn write.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "%s: create %s", registryRollbackMsg, dir)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "%s: stage %s", registryRollbackMsg, path)
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(name, 0o600)
	}
	if werr == nil {
		werr = os.Rename(name, path)
	}
	if werr != nil {
		_ = os.Remove(name)
		return cascade.Wrapf(cascade.KindUnavailable, werr, "%s: write %s", registryRollbackMsg, path)
	}
	return nil
}

// warnIfStale logs a warning naming the age when idx was published more
// than 90 days before the client's clock, or when its published_at does not
// parse. It never refuses: freshness is advisory (a frozen registry and a
// replayed latest index look the same from here).
func (c *RegistryClient) warnIfStale(idx RegistryIndex) {
	log := c.cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	published, err := time.Parse(time.RFC3339, idx.PublishedAt)
	if err != nil {
		log.Warn("registry index published_at is not RFC 3339; its age is unknown",
			"registry", c.cfg.RegistryURL, "published_at", idx.PublishedAt)
		return
	}
	age := c.clock.Now().Sub(published)
	if age <= registryStaleAfter {
		return
	}
	days := int(age / (24 * time.Hour))
	log.Warn(fmt.Sprintf("registry index is %d days old (published %s, older than 90 days): the registry may have stopped publishing or an old index is being replayed", days, idx.PublishedAt),
		"registry", c.cfg.RegistryURL, "age_days", days)
}
