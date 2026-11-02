package fusion

import (
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
)

// Record is the corpus-resolved classification attached to a vector hit.
type Record = corpus.Record

// validatePaths discards invalid provenance before scores are sorted or capped.
func validatePaths(hits []scoredHit, root func(string) (string, bool)) []scoredHit {
	out := hits[:0]
	for _, hit := range hits {
		path, ok := candidatePath(Record{CorpusID: hit.candidate.CorpusID}, hit.candidate.Path, root)
		if ok {
			hit.candidate.Path = path
			out = append(out, hit)
		}
	}
	return out
}

// candidatePath treats metadata as a relative source path, never as authority
// to select a corpus or its root. Containment is lexical; no files are opened.
func candidatePath(record Record, metaPath string, corpusRoot func(string) (string, bool)) (string, bool) {
	if filepath.IsAbs(metaPath) || filepath.VolumeName(metaPath) != "" {
		return "", false
	}
	clean := filepath.Clean(metaPath)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false
	}
	root := "."
	if corpusRoot != nil {
		var ok bool
		root, ok = corpusRoot(record.CorpusID)
		if !ok || root == "" {
			return "", false
		}
	}
	rel, err := filepath.Rel(root, filepath.Join(root, clean))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if metaPath == "" {
		return "", true
	}
	// Metadata paths are repo-relative slash paths on every OS; Clean uses the
	// OS separator, so convert back before the path leaves this package.
	return filepath.ToSlash(clean), true
}
