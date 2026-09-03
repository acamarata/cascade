//go:build darwin && cgo

// Purpose: darwin ElevationKeystore backend: LocalAuthentication (device
//
//	owner presence: biometrics or passcode) gates a single Ed25519
//	signing key held in the macOS Keychain.
//
// Inputs: none beyond the process's own Keychain/LocalAuthentication
//
//	access at call time.
//
// Outputs: ElevationKeystore's four operations, backed by real
//
//	Security.framework and LocalAuthentication.framework calls (Art.2
//	real-counterpart) via CGO with an Objective-C preamble
//	(-x objective-c CFLAGS; no separate .m file, so this stays inside
//	files_scope's single darwin entry).
//
// Constraints: Apple's Secure Enclave (SecKeyCreateRandomKey with
//
//	kSecAttrTokenIDSecureEnclave) supports only P-256 EC keys, never
//	Ed25519 — and this ticket's attestation format is verified by
//	internal/rpc/elevation_attest.go with ed25519.Verify, a format this
//	ticket does not get to change. A hardware-backed Ed25519 signing
//	operation is therefore not available from Apple's public APIs on
//	this platform: the Ed25519 keypair is generated in Go and its
//	private half is written into the Keychain behind a SecAccessControl
//	requiring device-owner presence (kSecAccessControlUserPresence,
//	kSecAttrAccessibleWhenUnlockedThisDeviceOnly), never plaintext on
//	disk and never exported by this package. That is TierOSKeychain, not
//	TierSecureEnclave — see this ticket's journal for the full Art.12
//	risk-spike finding this asymmetry produced. Auth and sign happen in
//	ONE call (cascade_signing_key_load evaluates LocalAuthentication and
//	reads the protected item using that same authenticated LAContext, so
//	the OS never prompts twice and no token crosses back into Go).
//	CGO is isolated to this file (darwin && cgo build tag); core and
//	every other platform build with CGO_ENABLED=0.
//
// SPORT: internal/elevation darwinKeystore/ADDED (P1-E04-W1-S07-T6).

package elevation

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework LocalAuthentication -framework Security

#import <Foundation/Foundation.h>
#import <LocalAuthentication/LocalAuthentication.h>
#include <Security/Security.h>
#include <string.h>

// cascade_la_probe reports (without any UI) whether device-owner
// authentication can be evaluated at all on this host. Used only by
// IsAvailable, never by Sign.
static int cascade_la_probe(void) {
    LAContext *ctx = [[LAContext alloc] init];
    NSError *err = nil;
    return [ctx canEvaluatePolicy:LAPolicyDeviceOwnerAuthentication error:&err] ? 1 : 0;
}

// cascade_keychain_store replaces service/account's item with data,
// protected by a device-owner-presence SecAccessControl when protect!=0,
// or stored with no access control (still device-locked at rest) when
// protect==0. Returns an OSStatus (errSecSuccess == 0 on success).
static OSStatus cascade_keychain_store(const char *service, const char *account,
                                        const unsigned char *data, int len, int protect) {
    NSString *svc = [NSString stringWithUTF8String:service];
    NSString *acct = [NSString stringWithUTF8String:account];
    NSData *blob = [NSData dataWithBytes:data length:len];

    NSMutableDictionary *key = [NSMutableDictionary dictionary];
    key[(__bridge id)kSecClass] = (__bridge id)kSecClassGenericPassword;
    key[(__bridge id)kSecAttrService] = svc;
    key[(__bridge id)kSecAttrAccount] = acct;
    SecItemDelete((__bridge CFDictionaryRef)key);

    NSMutableDictionary *add = [NSMutableDictionary dictionaryWithDictionary:key];
    add[(__bridge id)kSecValueData] = blob;
    if (protect) {
        CFErrorRef acErr = NULL;
        SecAccessControlRef ac = SecAccessControlCreateWithFlags(
            kCFAllocatorDefault, kSecAttrAccessibleWhenUnlockedThisDeviceOnly,
            kSecAccessControlUserPresence, &acErr);
        if (ac == NULL) {
            if (acErr) CFRelease(acErr);
            return errSecParam;
        }
        add[(__bridge id)kSecAttrAccessControl] = (__bridge id)ac;
        CFRelease(ac);
    } else {
        add[(__bridge id)kSecAttrAccessible] = (__bridge id)kSecAttrAccessibleWhenUnlockedThisDeviceOnly;
    }
    return SecItemAdd((__bridge CFDictionaryRef)add, NULL);
}

// cascade_keychain_load_plain reads an UNPROTECTED item (the public key)
// with no authentication involved. Returns the byte length on success, -1
// if no such item exists, -2 if it does not fit cap.
static int cascade_keychain_load_plain(const char *service, const char *account,
                                        unsigned char *out, int cap) {
    NSMutableDictionary *q = [NSMutableDictionary dictionary];
    q[(__bridge id)kSecClass] = (__bridge id)kSecClassGenericPassword;
    q[(__bridge id)kSecAttrService] = [NSString stringWithUTF8String:service];
    q[(__bridge id)kSecAttrAccount] = [NSString stringWithUTF8String:account];
    q[(__bridge id)kSecReturnData] = @YES;
    q[(__bridge id)kSecMatchLimit] = (__bridge id)kSecMatchLimitOne;

    CFTypeRef result = NULL;
    if (SecItemCopyMatching((__bridge CFDictionaryRef)q, &result) != errSecSuccess || result == NULL) {
        return -1;
    }
    NSData *data = (__bridge_transfer NSData *)result;
    if ((int)data.length > cap) return -2;
    memcpy(out, data.bytes, data.length);
    return (int)data.length;
}

// cascade_signing_key_load evaluates device-owner authentication and, ONLY
// on success, reads the protected private-key item using that SAME
// LAContext (kSecUseAuthenticationContext) so the OS does not prompt a
// second time. Returns the key length on success; -1 the policy cannot be
// evaluated on this host; -2 authentication failed or was canceled; -3 no
// key is enrolled; -4 the buffer is too small.
static int cascade_signing_key_load(const char *service, const char *account,
                                     const char *reason, unsigned char *out, int cap) {
    LAContext *ctx = [[LAContext alloc] init];
    NSError *policyErr = nil;
    if (![ctx canEvaluatePolicy:LAPolicyDeviceOwnerAuthentication error:&policyErr]) {
        return -1;
    }

    __block BOOL authed = NO;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    NSString *nsReason = [NSString stringWithUTF8String:reason];
    [ctx evaluatePolicy:LAPolicyDeviceOwnerAuthentication localizedReason:nsReason
                   reply:^(BOOL success, NSError *evalErr) {
        authed = success;
        dispatch_semaphore_signal(sem);
    }];
    dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);
    if (!authed) return -2;

    NSMutableDictionary *q = [NSMutableDictionary dictionary];
    q[(__bridge id)kSecClass] = (__bridge id)kSecClassGenericPassword;
    q[(__bridge id)kSecAttrService] = [NSString stringWithUTF8String:service];
    q[(__bridge id)kSecAttrAccount] = [NSString stringWithUTF8String:account];
    q[(__bridge id)kSecUseAuthenticationContext] = ctx;
    q[(__bridge id)kSecReturnData] = @YES;
    q[(__bridge id)kSecMatchLimit] = (__bridge id)kSecMatchLimitOne;

    CFTypeRef result = NULL;
    if (SecItemCopyMatching((__bridge CFDictionaryRef)q, &result) != errSecSuccess || result == NULL) {
        return -3;
    }
    NSData *data = (__bridge_transfer NSData *)result;
    if ((int)data.length > cap) return -4;
    memcpy(out, data.bytes, data.length);
    return (int)data.length;
}
*/
import "C"

import "unsafe"

const (
	darwinService    = "dev.cascade.elevation"
	darwinPrivAcct   = "ed25519-private-key"
	darwinPubAcct    = "ed25519-public-key"
	darwinAuthReason = "authorize this Cascade elevated action"
)

// darwinBridge is the seam between darwinKeystore's Go-side control flow
// (idempotency, error-code mapping, key generation, zeroing) and the raw
// CGO/Objective-C calls above. cgoBridge is the ONLY production
// implementation and is never stubbed in shipped code; keystore_darwin_
// test.go's fakeDarwinBridge (Art.1: _test.go only) lets this file's Go
// logic be exercised deterministically without touching the real Keychain
// or triggering a real LocalAuthentication prompt. This seam does NOT
// substitute for Art.2's real-counterpart obligation — it only isolates
// what CAN be unit-tested (this file's own logic) from what cannot
// (Security.framework/LocalAuthentication.framework's actual behavior,
// covered instead by keystore_darwin_integration_test.go).
type darwinBridge interface {
	laProbe() bool
	keychainStore(account string, data []byte, protect bool) int
	keychainLoadPlain(account string, capacity int) ([]byte, bool)
	signingKeyLoad(reason string, capacity int) (buf []byte, n int)
}

// cgoBridge is the real darwinBridge: every method is a direct, unmodified
// call into the Objective-C preamble above.
type cgoBridge struct{}

func (cgoBridge) laProbe() bool { return C.cascade_la_probe() == 1 }

func (cgoBridge) keychainStore(account string, data []byte, protect bool) int {
	p := C.int(0)
	if protect {
		p = 1
	}
	cSvc, cAcct := C.CString(darwinService), C.CString(account)
	defer C.free(unsafe.Pointer(cSvc))
	defer C.free(unsafe.Pointer(cAcct))
	status := C.cascade_keychain_store(cSvc, cAcct, (*C.uchar)(unsafe.Pointer(&data[0])), C.int(len(data)), p)
	return int(status)
}

func (cgoBridge) keychainLoadPlain(account string, capacity int) ([]byte, bool) {
	buf := make([]byte, capacity)
	cSvc, cAcct := C.CString(darwinService), C.CString(account)
	defer C.free(unsafe.Pointer(cSvc))
	defer C.free(unsafe.Pointer(cAcct))
	n := C.cascade_keychain_load_plain(cSvc, cAcct, (*C.uchar)(unsafe.Pointer(&buf[0])), C.int(capacity))
	if int(n) != capacity {
		return nil, false
	}
	return buf, true
}

func (cgoBridge) signingKeyLoad(reason string, capacity int) ([]byte, int) {
	buf := make([]byte, capacity)
	cSvc, cAcct, cReason := C.CString(darwinService), C.CString(darwinPrivAcct), C.CString(reason)
	defer C.free(unsafe.Pointer(cSvc))
	defer C.free(unsafe.Pointer(cAcct))
	defer C.free(unsafe.Pointer(cReason))
	n := C.cascade_signing_key_load(cSvc, cAcct, cReason, (*C.uchar)(unsafe.Pointer(&buf[0])), C.int(capacity))
	return buf, int(n)
}
