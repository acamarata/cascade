package promotion

import (
	"os"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/minisign"
	"github.com/acamarata/cascade/internal/runtime"
)

// trustedComment is the exact minisign trusted comment the release key signs
// cascade_checksums.txt with for a final tag ("cascade <version> checksums").
func trustedComment(finalTag string) string {
	return "cascade " + strings.TrimPrefix(finalTag, "v") + " checksums"
}

// verifySignature verifies the minisign signature over checksums with pub
// and returns it parsed. Every failure is a KindIntegrity refusal.
func verifySignature(checksums, signature []byte, pub minisign.PublicKey) (minisign.Signature, error) {
	sig, err := minisign.ParseSignature(signature)
	if err != nil {
		return minisign.Signature{}, refused("%s does not parse: %v", signatureName, err)
	}
	if err = minisign.Verify(pub, checksums, sig); err != nil {
		return minisign.Signature{}, refused("%s does not verify %s: %v", signatureName, checksumsName, err)
	}
	return sig, nil
}

// checkComment requires the verified trusted comment to bind finalTag.
func checkComment(sig minisign.Signature, finalTag string) error {
	if want := trustedComment(finalTag); sig.TrustedComment != want {
		return refused("%s trusted comment %q is not %q", signatureName, sig.TrustedComment, want)
	}
	return nil
}

// Build verifies the minisign signature over checksums FIRST, then parses
// the signed lines into a Record for rcTag at commit, stamped with the
// sha256 of the gate report at gateReport and the injected clock's time.
// Images stays empty: WithImages fills it from cascade_images.txt. A bad
// signature, a bad tag or commit, a malformed line, a missing platform or
// an unreadable gate report returns an error and a zero Record.
func Build(checksums, signature []byte, pub minisign.PublicKey, rcTag, commit string, gateReport string, clock runtime.Clock) (Record, error) {
	sig, err := verifySignature(checksums, signature, pub)
	if err != nil {
		return Record{}, err
	}
	final, err := finalFromRC(rcTag)
	if err != nil {
		return Record{}, err
	}
	if err = checkComment(sig, final); err != nil {
		return Record{}, err
	}
	if !commitRe.MatchString(commit) {
		return Record{}, invalid("commit %q is not 40 lowercase hex characters", commit)
	}
	sums, err := parseChecksums(checksums)
	if err != nil {
		return Record{}, err
	}
	archives, assets, err := classify(sums, strings.TrimPrefix(final, "v"))
	if err != nil {
		return Record{}, err
	}
	report, err := os.ReadFile(gateReport)
	if err != nil || gateReport == "" {
		return Record{}, invalid("gate report %q is unreadable", gateReport)
	}
	return Record{
		SchemaVersion: recordSchemaVersion, RCTag: rcTag, FinalTag: final, CommitSHA: commit,
		ChecksumsSHA256: digest(checksums), SignatureSHA256: digest(signature),
		Archives: archives, Assets: assets, Images: map[string]string{},
		GateReport: GateReport{Path: gateReport, SHA256: digest(report)},
		PromotedAt: clock.Now().UTC().Format(time.RFC3339),
	}, nil
}

// WithImages returns r with Images set from the bytes of cascade_images.txt,
// refusing bytes whose sha256 is not the signed checksums line r recorded
// for that file.
func WithImages(r Record, images []byte) (Record, error) {
	want, ok := r.Assets[imagesName]
	if !ok {
		return Record{}, refused("the record has no signed %s line", imagesName)
	}
	if digest(images) != want {
		return Record{}, refused("%s differs from its signed checksums line", imagesName)
	}
	parsed, err := parseImages(images)
	if err != nil {
		return Record{}, err
	}
	r.Images = parsed
	return r, nil
}
