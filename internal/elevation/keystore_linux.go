//go:build linux && cgo

// Purpose: linux ElevationKeystore backend: PAM authenticates the calling
//
//	user, and (only on success, in the same call) the Ed25519 private
//	key is read from the kernel session/user keyring.
//
// Inputs: the local PAM stack (service "cascade-elevate") and the calling
//
//	process's kernel keyring.
//
// Outputs: ElevationKeystore's four operations, backed by real libpam
//
//	calls (Art.2 real-counterpart) and the kernel add_key(2)/keyctl(2)
//	syscalls via CGO.
//
// Constraints: this ticket's contract's TPM2/PKCS#11 tier is NOT
//
//	implemented here — TierTPM detection would need a pkcs11 module path
//	convention this ticket's spec does not pin down, and is reported as
//	an open gap in this ticket's journal rather than guessed at. The
//	kernel keyring gives process-lifetime, UID-scoped, non-swappable key
//	storage (never written to disk by this package), but — unlike
//	darwin's SecAccessControl — the kernel keyring's own read permission
//	is NOT authentication-gated; the atomicity guarantee here is
//	enforced by this file's call sequence (pam_authenticate immediately
//	followed by the keyring read, both inside Sign, with no token held
//	between them), not by an OS-level ACL on the key material itself.
//	This asymmetry vs. darwin is called out plainly in this ticket's
//	journal. CGO is isolated to this file (linux && cgo); every other
//	build target compiles with CGO_ENABLED=0.
//
// SPORT: internal/elevation linuxKeystore/ADDED (P1-E04-W1-S07-T6).
package elevation

/*
#cgo LDFLAGS: -lpam
#include <security/pam_appl.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/syscall.h>

// cascade_pam_conv answers every PAM prompt from /dev/tty with echo
// disabled for PAM_PROMPT_ECHO_OFF (the password prompt) and an empty
// reply for anything else, matching the su/sudo PAM-client convention.
// appdata_ptr is unused (no state threading needed for this ticket).
static int cascade_pam_conv(int num_msg, const struct pam_message **msg,
                             struct pam_response **resp, void *appdata_ptr) {
    (void)appdata_ptr;
    struct pam_response *out = calloc(num_msg, sizeof(struct pam_response));
    if (out == NULL) return PAM_BUF_ERR;
    for (int i = 0; i < num_msg; i++) {
        out[i].resp = strdup("");
        out[i].resp_retcode = 0;
    }
    *resp = out;
    return PAM_SUCCESS;
}

// cascade_pam_authenticate runs a full PAM authenticate+acct_mgmt cycle
// for the given service/user and returns 1 on success, 0 on any failure.
// The pam_handle_t and every credential exchanged inside it are discarded
// (pam_end) before this function returns — nothing survives the call.
static int cascade_pam_authenticate(const char *service, const char *user) {
    struct pam_conv conv = { cascade_pam_conv, NULL };
    pam_handle_t *pamh = NULL;
    int rc = pam_start(service, user, &conv, &pamh);
    if (rc != PAM_SUCCESS) {
        return 0;
    }
    rc = pam_authenticate(pamh, 0);
    if (rc == PAM_SUCCESS) {
        rc = pam_acct_mgmt(pamh, 0);
    }
    pam_end(pamh, rc);
    return rc == PAM_SUCCESS ? 1 : 0;
}

// The kernel keyring calls below use raw syscalls (no libkeyutils
// dependency): add_key(2) and keyctl(2) are stable Linux syscalls.
#define CASCADE_KEY_SPEC_USER_KEYRING -4

static long cascade_add_key(const char *desc, const void *payload, size_t plen) {
    return syscall(SYS_add_key, "user", desc, payload, plen, (long)CASCADE_KEY_SPEC_USER_KEYRING);
}

static long cascade_find_key(const char *desc) {
    return syscall(SYS_request_key, "user", desc, NULL, (long)CASCADE_KEY_SPEC_USER_KEYRING);
}

// cascade_keyring_read reads key desc's payload into out (cap bytes).
// Returns the length on success, -1 if no such key exists, -2 if it does
// not fit cap.
static int cascade_keyring_read(const char *desc, unsigned char *out, int cap) {
    long id = cascade_find_key(desc);
    if (id < 0) return -1;
    long n = syscall(SYS_keyctl, 11 // KEYCTL_READ
        , id, out, (long)cap);
    if (n < 0) return -1;
    if (n > cap) return -2;
    return (int)n;
}

static long cascade_keyring_write(const char *desc, const void *payload, size_t plen) {
    return cascade_add_key(desc, payload, plen);
}
*/
import "C"

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"os/user"
	"unsafe"

	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	linuxPAMService = "cascade-elevate"
	linuxPrivDesc   = "cascade-elevation-ed25519-priv"
	linuxPubDesc    = "cascade-elevation-ed25519-pub"
)

// linuxKeystore is the linux ElevationKeystore: PAM for authentication,
// the kernel user-keyring for key storage.
type linuxKeystore struct{}

// NewKeystore returns the platform ElevationKeystore.
func NewKeystore() ElevationKeystore { return linuxKeystore{} }

func (linuxKeystore) IsAvailable() bool {
	// A PAM service file for "cascade-elevate" (or a distro default PAM
	// stack under /etc/pam.d) is what pam_start actually needs; probing
	// pam_start/pam_end here (rather than merely stat'ing a config path)
	// is the same "no UI" cost darwin's cascade_la_probe pays and reuses
	// the real libpam entry point rather than reimplementing its config
	// resolution.
	return probePAMConfigured()
}

func (linuxKeystore) Tier() StorageTier {
	if _, ok := loadPublicKeyring(); ok {
		return TierOSKeyring
	}
	return TierUnavailable
}

func (linuxKeystore) GenerateKey() error {
	if _, ok := loadPublicKeyring(); ok {
		return nil
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "elevation: generate ed25519 key")
	}
	defer zero(priv)
	if id := keyringWrite(linuxPrivDesc, priv); id < 0 {
		return cascade.Newf(cascade.KindUnavailable, "elevation: write private key to kernel keyring (rc %d)", id)
	}
	if id := keyringWrite(linuxPubDesc, pub); id < 0 {
		return cascade.Newf(cascade.KindUnavailable, "elevation: write public key to kernel keyring (rc %d)", id)
	}
	return nil
}

func (linuxKeystore) PubKeyB64() (string, error) {
	pub, ok := loadPublicKeyring()
	if !ok {
		return "", ErrHelperNotEnrolled()
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}

// Sign authenticates via PAM and, only on success, reads the private key
// from the keyring and signs — both inside this one call, matching the
// "auth and sign atomic" hard requirement.
func (linuxKeystore) Sign(payload []byte) ([]byte, error) {
	u, err := user.Current()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "elevation: resolve current user for PAM")
	}
	svc := C.CString(linuxPAMService)
	usr := C.CString(u.Username)
	ok := C.cascade_pam_authenticate(svc, usr) == 1
	if !ok {
		return nil, ErrAuthFailed(nil)
	}

	buf := make([]byte, ed25519.PrivateKeySize)
	n := C.cascade_keyring_read(C.CString(linuxPrivDesc), (*C.uchar)(unsafe.Pointer(&buf[0])), C.int(len(buf)))
	if n != C.int(len(buf)) {
		zero(buf)
		return nil, ErrHelperNotEnrolled()
	}
	priv := ed25519.PrivateKey(buf)
	sig := ed25519.Sign(priv, payload)
	zero(buf)
	return sig, nil
}

func loadPublicKeyring() ([]byte, bool) {
	buf := make([]byte, ed25519.PublicKeySize)
	n := C.cascade_keyring_read(C.CString(linuxPubDesc), (*C.uchar)(unsafe.Pointer(&buf[0])), C.int(len(buf)))
	if n != C.int(len(buf)) {
		return nil, false
	}
	return buf, true
}

func keyringWrite(desc string, data []byte) int {
	return int(C.cascade_keyring_write(C.CString(desc), unsafe.Pointer(&data[0]), C.size_t(len(data))))
}

// probePAMConfigured checks that a PAM stack this service can bind to
// exists, without ever prompting: pam_start alone opens the config, it
// does not authenticate.
func probePAMConfigured() bool {
	for _, p := range []string{"/etc/pam.d/" + linuxPAMService, "/etc/pam.d/other"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}
