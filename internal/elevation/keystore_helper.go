// Purpose: the darwin elevation helper keystore. A release cascade binary is
//
//	built with CGO_ENABLED=0 and cannot reach the Keychain or
//	LocalAuthentication itself; the cgo helper cascade-elevate-helper does,
//	and this file is the cascade side of that pair: it locates the helper,
//	checks the file, checks the signatures on both ends (keystore_helper_sig.go)
//	and speaks the one-line JSON protocol (keystore_helper_exec.go runs it).
//
// Inputs: the resolved cascade executable (the helper must sit beside it).
// Outputs: an ElevationKeystore whose every failure is KindUnavailable (no
//
//	usable helper) or the typed kind the helper reported, and helperSource,
//	the darwin platform-rank custody source (not registered yet).
//
// Constraints: no PATH lookup and no environment variable ever names the
//
//	helper; the helper is a regular, non-group/world-writable file owned by
//	this uid or root, found with Lstat (a symlink is refused); it is started
//	with an empty environment; no request byte is written until the code
//	running as the child has been verified (VerifyPID).
//
// SPORT: internal/elevation helperKeystore/ADDED (P1-SHIP-15).

package elevation

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// HelperName is the file name of the elevation helper beside the cascade binary.
const HelperName = "cascade-elevate-helper"

const (
	helperMaxLine    = 64 << 10
	helperMaxPayload = 4096
)

// Per-request time limits; vars only so tests can shorten them.
var (
	helperSignTimeout = 60 * time.Second
	helperOpTimeout   = 5 * time.Second
)

// helperRunner starts the helper at path with the socketpair protocol, calls
// vetChild(pid) while the child blocks on its first read, writes req only if
// that returned nil, and returns the single response line.
type helperRunner func(ctx context.Context, path string, req []byte, timeout time.Duration, vetChild func(pid int) error) ([]byte, error)

// Production wiring. Tests override these; the unix-only ones are set by
// keystore_helper_exec.go (nil elsewhere, which keeps the source unavailable).
var (
	helperExecutable                     = os.Executable
	helperVerifier           sigVerifier = newCodesignVerifier()
	platformHelperRun        helperRunner
	platformFileOwnerTrusted func(os.FileInfo) bool
	platformDirChain         func(helper string) error
	platformVolumeTrusted    func(path string) bool // false on a volume that ignores ownership
)

// HelperPromptReason is the sign prompt, built from the payload digest alone
// (no caller text). Cascade does not yet print that digest for the owner.
func HelperPromptReason(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "Cascade elevation " + hex.EncodeToString(sum[:])[:12]
}

type helperRequest struct {
	V          int    `json:"v"`
	Op         string `json:"op"`
	PayloadB64 string `json:"payload_b64,omitempty"`
}

type helperResponse struct {
	V         int    `json:"v"`
	OK        bool   `json:"ok"`
	Available bool   `json:"available"`
	Tier      string `json:"tier"`
	CGO       bool   `json:"cgo"`
	PubB64    string `json:"pub_b64"`
	SigB64    string `json:"sig_b64"`
	Kind      string `json:"kind"`
	Message   string `json:"message"`
}

type helperKeystore struct {
	executable func() (string, error)
	run        helperRunner
	verify     sigVerifier

	mu   sync.Mutex
	team string
}

func newHelperKeystore(executable func() (string, error), run helperRunner) ElevationKeystore {
	return newHelperKeystoreWith(executable, run, helperVerifier)
}

func newHelperKeystoreWith(executable func() (string, error), run helperRunner, verify sigVerifier) *helperKeystore {
	return &helperKeystore{executable: executable, run: run, verify: verify}
}

func helperUnavailable(cause error, msg string) error {
	return cascade.Wrap(cascade.KindUnavailable, cause, "elevation helper: "+msg)
}

// resolve returns the helper path or why it may not be used. The path is
// the directory of the real cascade executable plus HelperName; nothing else
// (no PATH, no environment) is consulted.
func (k *helperKeystore) resolve() (string, error) {
	exe, err := k.executable()
	if err != nil {
		return "", helperUnavailable(err, "cannot resolve the cascade executable")
	}
	if !filepath.IsAbs(exe) {
		return "", helperUnavailable(nil, "the cascade executable path is not absolute")
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return "", helperUnavailable(err, "cannot resolve the cascade executable")
	}
	path := filepath.Join(filepath.Dir(exe), HelperName)
	fi, err := os.Lstat(path)
	switch {
	case err != nil:
		return "", helperUnavailable(err, "not installed beside the cascade binary")
	case !fi.Mode().IsRegular():
		return "", helperUnavailable(nil, "not a regular file")
	case fi.Mode().Perm()&0o022 != 0:
		return "", helperUnavailable(nil, "group- or world-writable")
	case fi.Mode().Perm()&0o100 == 0:
		return "", helperUnavailable(nil, "not executable")
	case platformFileOwnerTrusted == nil || !platformFileOwnerTrusted(fi):
		return "", helperUnavailable(nil, "owned by another user")
	}
	// Whoever can write the file, its directory or one above it can swap it
	// between the checks and the exec; the swapped program would run as this
	// user before its signature is rejected.
	if platformDirChain == nil {
		return "", helperUnavailable(nil, "unsupported on this platform")
	}
	if err := platformDirChain(path); err != nil {
		return "", helperUnavailable(err, "can be changed by another user")
	}
	return path, nil
}

func (k *helperKeystore) selfTeam() (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.team != "" {
		return k.team, nil
	}
	team, err := k.verify.SelfTeam()
	if err != nil {
		return "", helperUnavailable(err, "cascade itself is not Developer ID signed")
	}
	k.team = team
	return team, nil
}

// call runs one request through the checked helper and decodes the answer.
func (k *helperKeystore) call(op string, payload []byte) (helperResponse, error) {
	var zeroResp helperResponse
	if k.run == nil {
		return zeroResp, helperUnavailable(nil, "unsupported on this platform")
	}
	path, err := k.resolve()
	if err != nil {
		return zeroResp, err
	}
	team, err := k.selfTeam()
	if err != nil {
		return zeroResp, err
	}
	if err := k.verify.VerifyPath(path, team); err != nil {
		return zeroResp, helperUnavailable(err, "signature check failed")
	}
	req := helperRequest{V: 1, Op: op}
	if payload != nil {
		req.PayloadB64 = base64.StdEncoding.EncodeToString(payload)
	}
	line, err := json.Marshal(req)
	if err != nil {
		return zeroResp, helperUnavailable(err, "encode request")
	}
	timeout := helperOpTimeout
	if op == "sign" || op == "delete" { // both wait for the owner at a prompt
		timeout = helperSignTimeout
	}
	vet := func(pid int) error { return k.verify.VerifyPID(pid, team) }
	raw, err := k.run(context.Background(), path, line, timeout, vet)
	if err != nil {
		return zeroResp, helperUnavailable(err, "request failed")
	}
	return decodeHelperResponse(raw)
}

// decodeHelperResponse bounds and decodes one helper reply; a failure becomes a typed error.
func decodeHelperResponse(raw []byte) (helperResponse, error) {
	var resp helperResponse
	if len(raw) > helperMaxLine {
		return resp, helperUnavailable(nil, "oversize response")
	}
	if err := json.Unmarshal(raw, &resp); err != nil || resp.V != 1 {
		return helperResponse{}, helperUnavailable(err, "malformed response")
	}
	if !resp.OK {
		return helperResponse{}, helperRemoteError(resp)
	}
	return resp, nil
}

// helperRemoteError maps the helper's failure kind to a cascade error; an
// unknown kind is treated as unavailable, never as success.
func helperRemoteError(resp helperResponse) error {
	kind := cascade.KindUnavailable
	for _, k := range cascade.AllKinds() {
		if k.String() == resp.Kind {
			kind = k
		}
	}
	return cascade.New(kind, "elevation helper: "+sanitizeHelperText(resp.Message))
}

// sanitizeHelperText keeps printable ASCII only, at most 200 runes.
func sanitizeHelperText(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r >= 0x20 && r < 0x7f && len(out) < 200 {
			out = append(out, r)
		}
	}
	return string(out)
}

func (k *helperKeystore) IsAvailable() bool {
	resp, err := k.call("status", nil)
	return err == nil && resp.Available && resp.CGO
}

func (k *helperKeystore) Tier() StorageTier {
	resp, err := k.call("status", nil)
	if err == nil && resp.Available && resp.CGO && resp.Tier == string(TierOSKeychain) {
		return TierOSKeychain
	}
	return TierUnavailable
}

func (k *helperKeystore) GenerateKey() error {
	_, err := k.call("generate", nil)
	return err
}

func (k *helperKeystore) PubKeyB64() (string, error) {
	resp, err := k.call("pubkey", nil)
	if err != nil {
		return "", err
	}
	pub, err := base64.StdEncoding.DecodeString(resp.PubB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return "", cascade.New(cascade.KindIntegrity, "elevation helper: malformed public key")
	}
	return resp.PubB64, nil
}

func (k *helperKeystore) Sign(payload []byte) ([]byte, error) {
	if len(payload) > helperMaxPayload {
		return nil, cascade.New(cascade.KindInvalidInput, "elevation helper: payload too large")
	}
	resp, err := k.call("sign", append([]byte{}, payload...))
	if err != nil {
		return nil, err
	}
	sig, err := base64.StdEncoding.DecodeString(resp.SigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, cascade.New(cascade.KindIntegrity, "elevation helper: malformed signature")
	}
	return sig, nil
}

// helperSource is the darwin platform-rank custody source backed by the
// helper, usable only when the helper resolves, passes the mutual signature
// checks and reports available && cgo. sourcesForOS does not register it yet
// (docs/elevation-helper.md, "Registration").
func helperSource() CustodySource {
	return CustodySource{Tier: CustodyPlatform, Name: "darwin-helper", Open: func(string) (ElevationKeystore, bool) {
		ks := newHelperKeystoreWith(helperExecutable, platformHelperRun, helperVerifier)
		return ks, ks.IsAvailable()
	}}
}
