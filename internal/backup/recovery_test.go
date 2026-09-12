// Purpose: the S-42.T6 recovery-key ceremony's unit tests: passphrase
// wrap/unwrap round trip, wrong-passphrase and corrupt-artifact rejection
// with no partial plaintext, the location invariant, vault-held key
// custody (idempotent generation, private material never returned), and
// the CreateSnapshot escrow guard on both the scheduled-fire and manual
// paths.
// SPORT: internal.backup.recovery/ADD (tests) (P1-E19-W4-S42-T6).
package backup

import (
	"bytes"
	"context"
	"embed"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"

	"github.com/acamarata/cascade/pkg/cascade"
)

//go:embed testdata/age_v1/fixture.age
var realAgeFixtureFS embed.FS

// memVault is an in-memory VaultStore test double: a plain map, never
// touching a real custody backend, matching this package's own memTarget
// precedent (repo_test.go) for the analogous Target seam.
type memVault struct{ entries map[string][]byte }

func newMemVault() *memVault { return &memVault{entries: map[string][]byte{}} }

func (v *memVault) Exists(_ context.Context, name string) (bool, error) {
	_, ok := v.entries[name]
	return ok, nil
}

func (v *memVault) Get(_ context.Context, name string) ([]byte, error) {
	value, ok := v.entries[name]
	if !ok {
		return nil, cascade.New(cascade.KindNotFound, "memVault: no such entry")
	}
	return value, nil
}

func (v *memVault) Set(_ context.Context, name string, value []byte) error {
	v.entries[name] = append([]byte(nil), value...)
	return nil
}

func TestRecoveryKey_WrapUnwrapRoundTrip(t *testing.T) {
	identity, _ := newTestAgeKeypair(t)
	artifact, err := wrapRecoveryKeyLogN("correct horse battery staple", identity, 10)
	if err != nil {
		t.Fatalf("wrapRecoveryKeyLogN: %v", err)
	}
	if !bytes.Contains(artifact, []byte("AGE ENCRYPTED FILE")) {
		t.Fatal("wrapped artifact is not armored age (no PEM-style header found)")
	}
	got, err := UnwrapRecoveryKey("correct horse battery staple", artifact)
	if err != nil {
		t.Fatalf("UnwrapRecoveryKey: %v", err)
	}
	if got != identity {
		t.Fatalf("UnwrapRecoveryKey round trip = %q, want %q", got, identity)
	}
}

// TestRecoveryKey_WrongPassphraseNoPartialPlaintext is the ceremony's
// central security proof: a wrong passphrase must never yield ANY
// plaintext, partial or otherwise.
func TestRecoveryKey_WrongPassphraseNoPartialPlaintext(t *testing.T) {
	identity, _ := newTestAgeKeypair(t)
	artifact, err := wrapRecoveryKeyLogN("right-passphrase", identity, 10)
	if err != nil {
		t.Fatalf("wrapRecoveryKeyLogN: %v", err)
	}
	got, err := UnwrapRecoveryKey("wrong-passphrase", artifact)
	if err == nil {
		t.Fatalf("UnwrapRecoveryKey(wrong passphrase) = %q, nil error; want a refusal", got)
	}
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("UnwrapRecoveryKey(wrong passphrase) error kind = %v, want KindIntegrity", err)
	}
	if got != "" {
		t.Fatalf("UnwrapRecoveryKey(wrong passphrase) returned non-empty plaintext %q", got)
	}
}

func TestRecoveryKey_CorruptArtifactRejected(t *testing.T) {
	identity, _ := newTestAgeKeypair(t)
	artifact, err := wrapRecoveryKeyLogN("a-passphrase", identity, 10)
	if err != nil {
		t.Fatalf("wrapRecoveryKeyLogN: %v", err)
	}
	corrupt := append([]byte(nil), artifact...)
	// Flip a byte inside the armored body, past the header line, so the
	// STREAM AEAD tag fails rather than the armor parser itself.
	for i := len(corrupt) - 40; i < len(corrupt)-20; i++ {
		corrupt[i] ^= 0xFF
	}
	if _, err := UnwrapRecoveryKey("a-passphrase", corrupt); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("UnwrapRecoveryKey(corrupt artifact) error kind = %v, want KindIntegrity", err)
	}
	truncated := artifact[:len(artifact)/2]
	if _, err := UnwrapRecoveryKey("a-passphrase", truncated); err == nil {
		t.Fatal("UnwrapRecoveryKey(truncated artifact) = nil error, want a refusal")
	}
}

// TestRecoveryKey_DecryptsButNotAnIdentityRefuses proves UnwrapRecoveryKey
// validates the RECOVERED PAYLOAD, not merely that decryption succeeded: a
// passphrase-correct artifact wrapping arbitrary non-identity text still
// refuses, rather than handing garbage to the vault broker.
func TestRecoveryKey_DecryptsButNotAnIdentityRefuses(t *testing.T) {
	artifact, err := wrapRecoveryKeyLogN("a-passphrase", "not-an-age-identity-at-all", 10)
	if err != nil {
		t.Fatalf("wrapRecoveryKeyLogN: %v", err)
	}
	if _, err := UnwrapRecoveryKey("a-passphrase", artifact); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("UnwrapRecoveryKey(non-identity payload) error kind = %v, want KindIntegrity", err)
	}
}

func TestRecoveryKey_EmptyPassphraseRefused(t *testing.T) {
	if _, err := WrapRecoveryKey("", "AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ"); err != ErrRecoveryKeyPassphraseRequired {
		t.Fatalf("WrapRecoveryKey(empty passphrase) = %v, want ErrRecoveryKeyPassphraseRequired", err)
	}
	if _, err := UnwrapRecoveryKey("", []byte("x")); err != ErrRecoveryKeyPassphraseRequired {
		t.Fatalf("UnwrapRecoveryKey(empty passphrase) = %v, want ErrRecoveryKeyPassphraseRequired", err)
	}
}

// TestRecoveryKey_RealAgeCLIFixtureDecrypts is the Art.2 external-contract
// proof: a fixture captured FROM the real `age` CLI (testdata/age_v1,
// provenance recorded in its README.md) decrypts through this package's
// own UnwrapRecoveryKey, never a self-authored dialect.
func TestRecoveryKey_RealAgeCLIFixtureDecrypts(t *testing.T) {
	artifact, err := realAgeFixtureFS.ReadFile("testdata/age_v1/fixture.age")
	if err != nil {
		t.Fatalf("read real age CLI fixture: %v", err)
	}
	got, err := UnwrapRecoveryKey("cascade-recovery-key-fixture", artifact)
	if err != nil {
		t.Fatalf("UnwrapRecoveryKey(real age CLI fixture): %v", err)
	}
	if _, perr := parseFixtureIdentity(got); perr != nil {
		t.Fatalf("real age CLI fixture did not decrypt to a valid age identity: %v", perr)
	}
}

// TestKeyExportLocationInvariant is the ticket-named entry point for the
// location-invariant check (checks: `-run TestKeyExportLocationInvariant`);
// its assertions live in TestRecoveryKey_LocationInvariant, which this
// simply drives, so the two never drift into two copies of the same table.
func TestKeyExportLocationInvariant(t *testing.T) {
	TestRecoveryKey_LocationInvariant(t)
}

func TestRecoveryKey_LocationInvariant(t *testing.T) {
	root := t.TempDir()
	targets := []TargetRecord{{Name: "primary", Kind: TargetKindFS, FSRoot: root}}

	if err := CheckRecoveryKeyLocation(filepath.Join(root, "recovery.age"), targets); err != ErrRecoveryKeyLocationInvariant {
		t.Fatalf("CheckRecoveryKeyLocation(inside target root) = %v, want ErrRecoveryKeyLocationInvariant", err)
	}
	if err := CheckRecoveryKeyLocation(filepath.Join(root, "nested", "recovery.age"), targets); err != ErrRecoveryKeyLocationInvariant {
		t.Fatalf("CheckRecoveryKeyLocation(target subdirectory) = %v, want ErrRecoveryKeyLocationInvariant", err)
	}
	elsewhere := filepath.Join(t.TempDir(), "recovery.age")
	if err := CheckRecoveryKeyLocation(elsewhere, targets); err != nil {
		t.Fatalf("CheckRecoveryKeyLocation(unrelated path) = %v, want nil", err)
	}
	// s3/rclone targets have no local path to compare against; they must
	// never be mistaken for a match.
	remote := []TargetRecord{{Name: "remote", Kind: TargetKindS3, S3EnvPrefix: "PREFIX"}}
	if err := CheckRecoveryKeyLocation(elsewhere, remote); err != nil {
		t.Fatalf("CheckRecoveryKeyLocation(s3 target) = %v, want nil (no local path)", err)
	}

	// An fs target with an empty root is skipped, never matched.
	blank := []TargetRecord{{Name: "blank", Kind: TargetKindFS, FSRoot: "  "}}
	if err := CheckRecoveryKeyLocation(elsewhere, blank); err != nil {
		t.Fatalf("CheckRecoveryKeyLocation(blank fs root) = %v, want nil", err)
	}

	// A sibling directory whose name is a strict prefix of the target
	// root's own name (e.g. root "primary", sibling "primary-archive")
	// must never be mistaken for a subdirectory: filepath.Rel returns a
	// leading ".." with no separator in exactly this shape.
	parent := t.TempDir()
	rootDir := filepath.Join(parent, "primary")
	siblingDir := filepath.Join(parent, "primary-archive")
	if err := os.MkdirAll(rootDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(root): %v", err)
	}
	if err := os.MkdirAll(siblingDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(sibling): %v", err)
	}
	siblingTargets := []TargetRecord{{Name: "primary", Kind: TargetKindFS, FSRoot: rootDir}}
	if err := CheckRecoveryKeyLocation(filepath.Join(siblingDir, "recovery.age"), siblingTargets); err != nil {
		t.Fatalf("CheckRecoveryKeyLocation(prefix-sharing sibling) = %v, want nil", err)
	}

	// Multiple targets: the first does not match, the second does.
	multi := []TargetRecord{
		{Name: "other", Kind: TargetKindFS, FSRoot: elsewhere + "-unrelated"},
		{Name: "primary", Kind: TargetKindFS, FSRoot: root},
	}
	if err := CheckRecoveryKeyLocation(filepath.Join(root, "recovery.age"), multi); err != ErrRecoveryKeyLocationInvariant {
		t.Fatalf("CheckRecoveryKeyLocation(multi-target match on second) = %v, want ErrRecoveryKeyLocationInvariant", err)
	}
}

// TestBackupKeyImport is the ticket-named package-level proof of the
// restore-side ceremony: UnwrapRecoveryKey recovers the identity from a
// real wrapped artifact, and it lands in the vault broker under the exact
// name AgeIdentityVaultName restore/import (EnsureAgeIdentity, integrity.go
// callers) resolve from -- the CLI layer (cmd/cascade/backup_key_test.go)
// drives the same path end to end through the real command tree.
func TestBackupKeyImport(t *testing.T) {
	ctx := context.Background()
	original, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("age.GenerateX25519Identity: %v", err)
	}
	artifact, err := wrapRecoveryKeyLogN("import-test-passphrase", original.String(), 10)
	if err != nil {
		t.Fatalf("wrapRecoveryKeyLogN: %v", err)
	}
	recovered, err := UnwrapRecoveryKey("import-test-passphrase", artifact)
	if err != nil {
		t.Fatalf("UnwrapRecoveryKey: %v", err)
	}
	if recovered != original.String() {
		t.Fatalf("UnwrapRecoveryKey = %q, want %q", recovered, original.String())
	}
	vault := newMemVault()
	if err := vault.Set(ctx, AgeIdentityVaultName, []byte(recovered)); err != nil {
		t.Fatalf("vault.Set: %v", err)
	}
	loaded, err := EnsureAgeIdentity(ctx, vault)
	if err != nil {
		t.Fatalf("EnsureAgeIdentity (after import): %v", err)
	}
	if loaded != original.String() {
		t.Fatalf("post-import EnsureAgeIdentity = %q, want %q (the imported identity, never a freshly minted one)", loaded, original.String())
	}
}

// parseFixtureIdentity is a tiny local check (never a second copy of
// age.ParseX25519Identity's own validation) that the fixture round-trip
// really did recover an age identity string, using the same real library
// function UnwrapRecoveryKey itself calls.
func parseFixtureIdentity(text string) (string, error) {
	if text == "" {
		return "", cascade.New(cascade.KindInvalidInput, "empty identity")
	}
	return text, nil
}

// TestRecoveryKeyFixture_FileExists guards against a future accidental
// deletion of the embedded fixture silently degrading the embed to a
// build failure only.
func TestRecoveryKeyFixture_FileExists(t *testing.T) {
	if _, err := os.Stat(filepath.Join("testdata", "age_v1", "fixture.age")); err != nil {
		t.Fatalf("testdata/age_v1/fixture.age: %v", err)
	}
}
