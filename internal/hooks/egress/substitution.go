package egress

import (
	"bytes"
	"context"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// exactValueClass is the detection class the exact-value pass labels its
// hits with, which decides the tag type through secrets.TagFor.
//
// A vault entry records a name and bytes, not a credential kind, so there
// is no true class to report. The generic apikey tag is used because
// rehydration keys on the NAME and the type is descriptive only; picking
// a kind the vault never stated would be a claim this package cannot
// support.
const exactValueClass = secrets.ClassAPIKey

// SubstitutionPass replaces credential material in content with typed
// vault-reference tags and returns the substituted bytes.
//
// It runs two passes, in this order:
//
//  1. EXACT VALUE. Every value the vault holds is matched as a literal
//     substring and tagged unconditionally: no entropy floor, no
//     confidence threshold, no detector opinion. A secret the operator
//     stored is a secret whatever it looks like, and the shapeless
//     passphrase is exactly the case a shape-based detector misses.
//  2. DETECTOR. The internal/secrets Detector then finds credential
//     material the vault does not know about, at the confidence the
//     operator configured. Spans already behind a tag from pass 1 are
//     left alone by the rewriter, so the two passes cannot double-tag one
//     secret.
//
// It fails closed. A vault read error, a rewrite error and content that
// is not valid UTF-8 all return a nil result with the error. There is no
// path that returns partially substituted bytes.
func SubstitutionPass(ctx context.Context, vault Vault, detector *secrets.Detector, rewriter *secrets.Rewriter, content []byte) ([]byte, error) {
	if vault == nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, ErrNoVault, "egress: substitution needs a vault")
	}
	if detector == nil || rewriter == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "egress: substitution needs a detector and a rewriter")
	}
	afterExact, err := exactValuePass(ctx, vault, rewriter, content)
	if err != nil {
		return nil, err
	}
	return detectorPass(detector, rewriter, afterExact)
}

// exactValuePass performs pass 1.
func exactValuePass(ctx context.Context, vault Vault, rewriter *secrets.Rewriter, content []byte) ([]byte, error) {
	values, err := loadVaultValues(ctx, vault)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err,
			"egress: reading the vault for the exact-value pass failed; nothing is written")
	}
	text := content
	for _, entry := range values {
		hits := exactHits(text, entry)
		if len(hits) == 0 {
			continue
		}
		result, rerr := rewriter.Rewrite(text, hits)
		if rerr != nil {
			return nil, cascade.Wrap(cascade.KindIntegrity, rerr,
				"egress: the exact-value pass could not be applied; nothing is written")
		}
		text = result.Text
	}
	return text, nil
}

// exactHits locates every occurrence of one stored value in text.
func exactHits(text []byte, entry vaultValue) []secrets.DetectionHit {
	var hits []secrets.DetectionHit
	name := vaultRefName(entry.name)
	for offset := 0; offset < len(text); {
		idx := bytes.Index(text[offset:], entry.value)
		if idx < 0 {
			break
		}
		start := offset + idx
		hits = append(hits, secrets.DetectionHit{
			Class:         exactValueClass,
			Pattern:       "vault-exact",
			Offset:        start,
			Len:           len(entry.value),
			Confidence:    secrets.ConfidenceProven,
			SuggestedName: name,
		})
		offset = start + len(entry.value)
	}
	return hits
}

// detectorPass performs pass 2 over the already exact-substituted text.
func detectorPass(detector *secrets.Detector, rewriter *secrets.Rewriter, text []byte) ([]byte, error) {
	hits := detector.ScanCertain(text)
	result, err := rewriter.Rewrite(text, hits)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindIntegrity, err,
			"egress: the detector pass could not be applied; nothing is written")
	}
	if result.Tainted {
		return nil, cascade.New(cascade.KindIntegrity,
			"egress: the rewriter reported the content still tainted; nothing is written")
	}
	return result.Text, nil
}
