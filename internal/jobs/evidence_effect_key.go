package jobs

// Purpose: the audit effect keys of evidence appends and their inverse.
//
//	The key is "jobs.evidence:<job_id>:<idempotency_key>" with every byte
//	outside [A-Za-z0-9.-] escaped as _xx (lowercase hex), so ':' stays a
//	pure separator and two distinct (job, key) pairs never share a key. A
//	pair too long for the effect log's 128-byte limit uses
//	"jobs.evidence-h:<sha256 of the escaped pair>" instead. A retry after a
//	failed attempt appends ":<n>" (a begun key is begun for good).
//
// Inputs: a job id and an idempotency key.
// Outputs: the effect key, or the pair (or hash) a pending key names.
// Constraints: parsing accepts only keys evidenceEffectKey itself produces
//
//	(canonical escapes, canonical attempt numbers, 64 lowercase hex), so a
//	key another site minted is never settled as evidence.
//
// SPORT: jobs/completion-gate/ADD (P1-SEC-21).

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

const (
	evidenceEffectPrefix     = "jobs.evidence:"
	evidenceEffectHashPrefix = "jobs.evidence-h:"
	evidenceEffectMaxKey     = 128
)

// escapeEffectPart keeps [A-Za-z0-9.-] and writes every other byte as _xx.
func escapeEffectPart(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "_%02x", c)
	}
	return b.String()
}

// canonicalEffectPart reverses escapeEffectPart. ok is false for an empty
// part, a bad escape, or any spelling escapeEffectPart would not produce.
func canonicalEffectPart(s string) (string, bool) {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '_' {
			out = append(out, s[i])
			continue
		}
		if i+3 > len(s) {
			return "", false
		}
		v, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
		if err != nil {
			return "", false
		}
		out = append(out, byte(v))
		i += 2
	}
	return string(out), len(out) > 0 && escapeEffectPart(string(out)) == s
}

// evidencePairHash is the injective digest of a (job, idempotency key) pair.
func evidencePairHash(jobID, idem string) string {
	sum := sha256.Sum256([]byte(escapeEffectPart(jobID) + ":" + escapeEffectPart(idem)))
	return hex.EncodeToString(sum[:])
}

// evidenceEffectKey derives attempt's effect key for one evidence row.
func evidenceEffectKey(jobID, idem string, attempt int) string {
	key := evidenceEffectPrefix + escapeEffectPart(jobID) + ":" + escapeEffectPart(idem)
	if len(key)+len(":16") > evidenceEffectMaxKey {
		key = evidenceEffectHashPrefix + evidencePairHash(jobID, idem)
	}
	if attempt > 0 {
		key += ":" + strconv.Itoa(attempt)
	}
	return key
}

// effectTarget names the row a pending evidence effect stands for: the
// pair (job, idem), or only its hash when the key was too long to spell.
type effectTarget struct{ job, idem, hash string }

// parseEvidenceEffectKey reads a key back; ok is false for a foreign,
// malformed or non-canonical key.
func parseEvidenceEffectKey(key string) (effectTarget, bool) {
	if rest, found := strings.CutPrefix(key, evidenceEffectHashPrefix); found {
		parts := strings.Split(rest, ":")
		raw, err := hex.DecodeString(parts[0])
		ok := err == nil && len(raw) == sha256.Size && hex.EncodeToString(raw) == parts[0]
		return effectTarget{hash: parts[0]}, ok && attemptSuffixOK(parts[1:])
	}
	rest, found := strings.CutPrefix(key, evidenceEffectPrefix)
	parts := strings.Split(rest, ":")
	if !found || len(parts) < 2 || len(parts) > 3 || !attemptSuffixOK(parts[2:]) ||
		len(evidenceEffectPrefix+parts[0]+":"+parts[1])+len(":16") > evidenceEffectMaxKey {
		return effectTarget{}, false // a pair this long is minted in the hashed form
	}
	job, ok1 := canonicalEffectPart(parts[0])
	idem, ok2 := canonicalEffectPart(parts[1])
	return effectTarget{job: job, idem: idem}, ok1 && ok2
}

// attemptSuffixOK accepts no suffix, or one canonical attempt number the
// minter can reach (1 up to evidenceEffectMaxTries-1).
func attemptSuffixOK(suffix []string) bool {
	if len(suffix) == 0 {
		return true
	}
	if len(suffix) > 1 {
		return false
	}
	n, err := strconv.Atoi(suffix[0])
	return err == nil && n > 0 && n < evidenceEffectMaxTries && strconv.Itoa(n) == suffix[0]
}
