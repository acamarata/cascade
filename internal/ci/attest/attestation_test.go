package attest

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/testkit"
)

// TestStart is the fixed start time every sample record uses.
var TestStart = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// TestNode is one enrolled node whose key lives in a file custody under a
// fresh temp HOME; tests never touch the real keychain.
type TestNode struct {
	ID      nodes.Identity
	Keys    *nodes.NodeKeystore
	Records *nodes.RecordStore
	Home    string
}

// NewTestNode enrolls a fresh runtime-generated identity. It fails the test
// unless the custody that answered is the temp-dir file vault.
func NewTestNode(t *testing.T) *TestNode {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	keys, err := nodes.NewNodeKeystore(secrets.Config{Service: "cascade-attest-test", Dir: home, ForceFileVault: true})
	if err != nil {
		t.Fatalf("NewNodeKeystore: %v", err)
	}
	if keys.BackendName() != "file-vault" {
		t.Fatalf("custody backend = %q, want the temp-dir file-vault", keys.BackendName())
	}
	id, priv, err := nodes.GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if err := keys.Store(context.Background(), id.NodeID, priv); err != nil {
		t.Fatalf("Store: %v", err)
	}
	records := nodes.NewRecordStore(nodes.NewFileRecordBackend(home), testkit.NewFrozenClock(TestStart))
	if _, err := records.Enroll(id, nodes.TierWorkerTrusted); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	return &TestNode{ID: id, Keys: keys, Records: records, Home: home}
}

// Sign is the node's custody signing function, as production binds it.
func (n *TestNode) Sign(payload []byte) ([]byte, error) {
	return n.Keys.Sign(context.Background(), n.ID.NodeID, payload)
}

// Trust is the record-backed trust store over the node's records.
func (n *TestNode) Trust() TrustStore { return NewRecordTrustStore(n.Records) }

// SampleRecord is a valid, passing record of plan kinds lint and unit.
func (n *TestNode) SampleRecord() AttestationRecord {
	return AttestationRecord{
		SchemaVersion: 1, ArtifactHashes: []string{"sha256:" + strings.Repeat("e", 64), "sha256:" + strings.Repeat("d", 64)},
		AttestorClass: ClassEnrolledNode, CheckpointID: "ckpt-1", CommitSHA: strings.Repeat("a1", 20),
		EndedAt: TestStart.Add(10 * time.Minute), ExitStatus: 0, ExpiresAt: TestStart.Add(12 * time.Hour),
		Environment: Environment{RunnerKind: "local", IsolationClass: "process", NodeID: n.ID.NodeID, ToolchainFP: "go1.26"},
		JobSpecHash: strings.Repeat("3c", 32), KeyID: nodes.KeyIDFor(n.ID.PubKey), NodeID: n.ID.NodeID,
		Nonce: strings.Repeat("0f", 16), PolicyVersion: "policy-v1", RepoID: "repo-a",
		ResultHashes: []string{"unit:" + strings.Repeat("2", 64), "lint:" + strings.Repeat("1", 64)},
		StartedAt:    TestStart, TargetSelection: "all", ToolchainFP: "go1.26", TreeHash: strings.Repeat("b2", 20),
	}
}

// MustAttest signs r through the node's custody.
func (n *TestNode) MustAttest(t *testing.T, r AttestationRecord) SignedAttestation {
	t.Helper()
	sa, err := Attest(r, n.Sign)
	if err != nil {
		t.Fatalf("Attest: %v", err)
	}
	return sa
}

// IsSentinel reports whether target appears by identity on err's chain.
func IsSentinel(err, target error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if err == target {
			return true
		}
	}
	return false
}

func TestAttestRoundTrip(t *testing.T) {
	n := NewTestNode(t)
	sa := n.MustAttest(t, n.SampleRecord())
	if err := VerifySignature(context.Background(), sa, n.SampleRecord().CommitSHA, n.SampleRecord().TreeHash, n.Trust(), TestStart.Add(time.Hour)); err != nil {
		t.Fatalf("VerifySignature(fresh attestation) = %v, want nil", err)
	}
	got, err := DecodeCanonical(sa.CanonicalJSON)
	if err != nil || !reflect.DeepEqual(got, sa.Record) {
		t.Fatalf("DecodeCanonical = %+v, %v; want the attested record %+v", got, err, sa.Record)
	}
	if !strings.HasPrefix(string(sa.CanonicalJSON), `{"artifact_hashes":["sha256:dddd`) {
		t.Fatalf("canonical bytes do not start with sorted artifact hashes: %s", sa.CanonicalJSON)
	}
	if RowID(sa) != RowID(SignedAttestation{CanonicalJSON: append([]byte(nil), sa.CanonicalJSON...)}) || len(RowID(sa)) != 64 {
		t.Fatalf("RowID = %q, want a stable 64-hex digest of the canonical bytes", RowID(sa))
	}
}

func TestAttestTamper(t *testing.T) {
	n := NewTestNode(t)
	r := n.SampleRecord()
	sa := n.MustAttest(t, r)
	i := bytes.Index(sa.CanonicalJSON, []byte(`"exit_status":0`)) + len(`"exit_status":`)
	tampered := sa
	tampered.CanonicalJSON = append([]byte(nil), sa.CanonicalJSON...)
	tampered.CanonicalJSON[i] = '1'
	err := VerifySignature(context.Background(), tampered, r.CommitSHA, r.TreeHash, n.Trust(), TestStart.Add(time.Hour))
	if !IsSentinel(err, ErrInvalidSignature) {
		t.Fatalf("VerifySignature(one byte changed) = %v, want ErrInvalidSignature", err)
	}
	sig, _ := base64.RawStdEncoding.DecodeString(sa.Signature)
	sig[0] ^= 0x01
	tampered = sa
	tampered.Signature = base64.RawStdEncoding.EncodeToString(sig)
	err = VerifySignature(context.Background(), tampered, r.CommitSHA, r.TreeHash, n.Trust(), TestStart.Add(time.Hour))
	if !IsSentinel(err, ErrInvalidSignature) {
		t.Fatalf("VerifySignature(signature bit flipped) = %v, want ErrInvalidSignature", err)
	}
}

func TestAttestZeroRequiredField(t *testing.T) {
	n := NewTestNode(t)
	zeroes := map[string]func(*AttestationRecord){
		"policy_version": func(r *AttestationRecord) { r.PolicyVersion = "" },
		"checkpoint_id":  func(r *AttestationRecord) { r.CheckpointID = "" },
		"nonce":          func(r *AttestationRecord) { r.Nonce = "" },
		"expires_at":     func(r *AttestationRecord) { r.ExpiresAt = time.Time{} },
		"commit_sha":     func(r *AttestationRecord) { r.CommitSHA = "" },
		"tree_hash":      func(r *AttestationRecord) { r.TreeHash = "" },
		"node_id":        func(r *AttestationRecord) { r.NodeID = "" },
		"key_id":         func(r *AttestationRecord) { r.KeyID = " " },
	}
	for name, zero := range zeroes {
		r := n.SampleRecord()
		zero(&r)
		if _, err := CanonicalJSON(r); !IsSentinel(err, ErrInvalidRecord) {
			t.Errorf("CanonicalJSON(empty %s) = %v, want ErrInvalidRecord", name, err)
		}
		if _, err := Attest(r, n.Sign); !IsSentinel(err, ErrInvalidRecord) {
			t.Errorf("Attest(empty %s) = %v, want ErrInvalidRecord", name, err)
		}
	}
}

func TestAttestationFieldListVersioned(t *testing.T) {
	var tags []string
	rt := reflect.TypeFor[AttestationRecord]()
	for i := range rt.NumField() {
		tags = append(tags, rt.Field(i).Tag.Get("json"))
	}
	want := slices.Clone(AttestationFieldsV1)
	slices.Sort(tags)
	slices.Sort(want)
	if !slices.Equal(tags, want) {
		t.Fatalf("struct json tags %v != AttestationFieldsV1 %v", tags, AttestationFieldsV1)
	}
	n := NewTestNode(t)
	canon := n.MustAttest(t, n.SampleRecord()).CanonicalJSON
	cases := map[string][]byte{
		"unknown field":  bytes.Replace(canon, []byte(`{"artifact_hashes"`), []byte(`{"aaa_extra":1,"artifact_hashes"`), 1),
		"schema 2":       bytes.Replace(canon, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1),
		"schema 0":       bytes.Replace(canon, []byte(`"schema_version":1`), []byte(`"schema_version":0`), 1),
		"whitespace":     bytes.Replace(canon, []byte(`,"attestor_class"`), []byte(`, "attestor_class"`), 1),
		"duplicate key":  append(bytes.TrimSuffix(bytes.Clone(canon), []byte(`}`)), []byte(`,"nonce":"x"}`)...),
		"case variant":   bytes.Replace(canon, []byte(`"nonce"`), []byte(`"Nonce"`), 1),
		"not an object":  []byte(`[]`),
		"unsorted array": bytes.Replace(canon, []byte(`"lint:`), []byte(`"zint:`), 1),
	}
	for name, b := range cases {
		if _, err := DecodeCanonical(b); !IsSentinel(err, ErrInvalidRecord) {
			t.Errorf("DecodeCanonical(%s) = %v, want ErrInvalidRecord", name, err)
		}
	}
}

func TestAttestationDomainPrefix(t *testing.T) {
	n := NewTestNode(t)
	r := n.SampleRecord()
	sa := n.MustAttest(t, r)
	sig, _ := base64.RawStdEncoding.DecodeString(sa.Signature)
	if !ed25519.Verify(n.ID.PubKey, append([]byte("cascade.ci.attestation.v1\x1f"), sa.CanonicalJSON...), sig) {
		t.Fatal("signature is not over the domain prefix plus canonical bytes")
	}
	for name, payload := range map[string][]byte{
		"bare canonical bytes": sa.CanonicalJSON,
		"heartbeat payload":    []byte(`{"node_id":"` + n.ID.NodeID + `","seq":1}`),
		"attest.sign request":  []byte(`{"method":"ci.attest.sign","params":` + string(sa.CanonicalJSON) + `}`),
	} {
		other, err := n.Sign(payload)
		if err != nil {
			t.Fatalf("Sign(%s): %v", name, err)
		}
		forged := sa
		forged.Signature = base64.RawStdEncoding.EncodeToString(other)
		if err := VerifySignature(context.Background(), forged, r.CommitSHA, r.TreeHash, n.Trust(), TestStart.Add(time.Hour)); !IsSentinel(err, ErrInvalidSignature) {
			t.Errorf("signature over the %s verified as an attestation: %v", name, err)
		}
	}
}

// TestAttestNeverDerivesRepoID: no production file of this package reaches
// git, a filesystem path or a repo-id helper; repo_id is only ever copied.
func TestAttestNeverDerivesRepoID(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	banned := []string{"os/exec", "path/filepath", "os", "io/fs"}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			for _, b := range banned {
				if strings.Trim(imp.Path.Value, `"`) == b {
					t.Errorf("%s imports %s; attestation code must not derive a repo id", f, b)
				}
			}
		}
		for _, ref := range []string{"LocalRepoID", "rev-list", "root-commit", "RepoIDFor", "DeriveRepoID"} {
			if bytes.Contains(src, []byte(ref)) {
				t.Errorf("%s references %s", f, ref)
			}
		}
	}
	n := NewTestNode(t)
	r := n.SampleRecord()
	r.RepoID = "registry-id-from-the-fact"
	if got := n.MustAttest(t, r).Record.RepoID; got != r.RepoID {
		t.Fatalf("attested repo_id = %q, want exactly the value passed in", got)
	}
}

func TestFailedRerunAttestationNotEligible(t *testing.T) {
	n := NewTestNode(t)
	plan := []string{"lint", "unit"}
	if !n.SampleRecord().Passing(plan) {
		t.Fatal("sample record is not Passing; fixture broken")
	}
	failed := n.SampleRecord()
	failed.ExitStatus = 1
	missing := n.SampleRecord()
	missing.ResultHashes = missing.ResultHashes[:1]
	emptyDigest := n.SampleRecord()
	emptyDigest.ResultHashes = []string{"lint:", "unit:" + strings.Repeat("2", 64)}
	nested := n.SampleRecord()
	nested.ResultHashes = []string{"lint:" + strings.Repeat("1", 64), "unit:x:" + strings.Repeat("2", 64)}
	for name, r := range map[string]AttestationRecord{"exit 1": failed, "missing kind": missing, "empty digest": emptyDigest, "other kind's hash": nested} {
		sa := n.MustAttest(t, r)
		if err := VerifySignature(context.Background(), sa, r.CommitSHA, r.TreeHash, n.Trust(), TestStart.Add(time.Hour)); err != nil {
			t.Fatalf("%s: a correctly signed record must still verify: %v", name, err)
		}
		if sa.Record.Passing(plan) {
			t.Errorf("%s: Passing = true, want false", name)
		}
	}
	if n.SampleRecord().Passing(nil) || nested.Passing([]string{"lint", "unit:x"}) {
		t.Error("Passing(empty plan or a kind containing ':') = true, want false")
	}
}
