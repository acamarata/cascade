//go:build darwin && cgo

// Purpose: the helper's caller check (R12 F-3, R12b G-2). Every op except
//
//	status is served only to a peer that is Developer ID signed by this
//	helper's own team. The peer (the process that owns the listener this
//	helper connected to) is identified by the audit token of the connection
//	(LOCAL_PEERTOKEN), which carries the pid AND the pid version,
//	and resolved with SecCodeCopyGuestWithAttributes(kSecGuestAttributeAudit):
//	a process that created the listener and then exec'd another binary no
//	longer matches its token, so it cannot borrow a signed binary's identity.
//	PID-based identity (getppid) is never used.
//
// Inputs: the connection to the listener cascade owns.
// Outputs: nil, or a permission-denied error before any keychain call.
//
// Constraints: the team is read from this helper's own signature at run time
//
//	(none in tracked files) and placed in the requirement only after the
//	elevation package's [A-Z0-9]{10} validation; an unsigned or ad-hoc helper
//	has no team and refuses every caller; the caller must also carry the
//	cascade signing identifier and the hardened runtime; all failures read
//	identically.
//
// SPORT: cmd/cascade-elevate-helper callerCheck/ADDED (P1-SHIP-15).
package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework Security

#import <Foundation/Foundation.h>
#include <Security/Security.h>
#include <Security/SecTask.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <bsm/libbsm.h>

#ifndef LOCAL_PEERTOKEN
#define LOCAL_PEERTOKEN 0x006
#endif

// cascade_self_team copies this process's Developer ID team identifier into
// out. Returns its length, or a negative value when this process is unsigned,
// ad-hoc signed, invalid or has no team.
static int cascade_self_team(char *out, int cap) {
    SecCodeRef self = NULL;
    SecStaticCodeRef staticCode = NULL;
    CFDictionaryRef info = NULL;
    int rc = -1;
    if (SecCodeCopySelf(kSecCSDefaultFlags, &self) != errSecSuccess) goto done;
    if (SecCodeCheckValidity(self, kSecCSStrictValidate, NULL) != errSecSuccess) goto done;
    if (SecCodeCopyStaticCode(self, kSecCSDefaultFlags, &staticCode) != errSecSuccess) goto done;
    if (SecCodeCopySigningInformation(staticCode, kSecCSSigningInformation, &info) != errSecSuccess) goto done;
    CFStringRef team = CFDictionaryGetValue(info, kSecCodeInfoTeamIdentifier);
    if (team == NULL || !CFStringGetCString(team, out, cap, kCFStringEncodingUTF8)) goto done;
    rc = (int)strlen(out);
done:
    if (info) CFRelease(info);
    if (staticCode) CFRelease(staticCode);
    if (self) CFRelease(self);
    return rc;
}

// cascade_hardened reports whether the RUNNING code carries the hardened
// runtime flag. The dynamic status is read, not the file's signature: the
// file at the process's path may have been swapped. (Injection and debugging
// entitlements are part of the requirement, which is judged dynamically too.)
static int cascade_hardened(SecCodeRef code) {
    CFDictionaryRef info = NULL;
    int ok = 0;
    if (SecCodeCopySigningInformation(code, kSecCSDynamicInformation, &info) == errSecSuccess) {
        CFNumberRef status = CFDictionaryGetValue(info, kSecCodeInfoStatus);
        uint32_t value = 0;
        if (status != NULL && CFNumberGetValue(status, kCFNumberSInt32Type, &value)) ok = (value & kSecCodeSignatureRuntime) != 0;
    }
    if (info) CFRelease(info);
    return ok;
}

// cascade_has_storage_entitlement reports whether this process carries a
// keychain-access-groups or application-identifier entitlement, which on
// macOS can only come from an embedded provisioning profile and which the
// Keychain needs before it stores an item with an access control.
static int cascade_has_storage_entitlement(void) {
    SecTaskRef task = SecTaskCreateFromSelf(NULL);
    if (task == NULL) return 0;
    int ok = 0;
    CFStringRef names[2] = {CFSTR("keychain-access-groups"), CFSTR("com.apple.application-identifier")};
    for (int i = 0; i < 2 && !ok; i++) {
        CFTypeRef value = SecTaskCopyValueForEntitlement(task, names[i], NULL);
        if (value != NULL) { ok = 1; CFRelease(value); }
    }
    CFRelease(task);
    return ok;
}

// cascade_peer_token copies the audit token of the peer of socket fd into
// out (32 bytes). The token names the process that last operated on the peer
// end and carries its pid version, but it is read LIVE: after that process
// execs, a new read shows the new image, which is why callers read it twice.
static int cascade_peer_token(int fd, unsigned char *out) {
    audit_token_t token;
    socklen_t len = sizeof(token);
    if (getsockopt(fd, 0, LOCAL_PEERTOKEN, &token, &len) != 0 || len != sizeof(token)) return -1;
    memcpy(out, &token, sizeof(token));
    return 0;
}

// cascade_check_token returns 0 when the process the audit token names
// satisfies the code requirement (and, when requireRuntime is set, is running
// with the hardened runtime), a negative value otherwise. The getsockopt level
// 0 above is SOL_LOCAL.
static int cascade_check_token(const unsigned char *tokenBytes, const char *requirement, int requireRuntime) {
    NSData *data = [NSData dataWithBytes:tokenBytes length:sizeof(audit_token_t)];
    NSDictionary *attrs = @{(__bridge id)kSecGuestAttributeAudit: data};
    SecCodeRef peer = NULL;
    if (SecCodeCopyGuestWithAttributes(NULL, (__bridge CFDictionaryRef)attrs, kSecCSDefaultFlags, &peer) != errSecSuccess) return -2;
    SecRequirementRef req = NULL;
    NSString *text = [NSString stringWithUTF8String:requirement];
    int rc = -3;
    if (SecRequirementCreateWithString((__bridge CFStringRef)text, kSecCSDefaultFlags, &req) == errSecSuccess) {
        rc = SecCodeCheckValidity(peer, kSecCSStrictValidate, req) == errSecSuccess ? 0 : -4;
        CFRelease(req);
    }
    if (rc == 0 && requireRuntime && !cascade_hardened(peer)) rc = -5;
    CFRelease(peer);
    return rc;
}
*/
import "C"

import (
	"bytes"
	"unsafe"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/pkg/cascade"
)

func errCallerRefused() error {
	return cascade.New(cascade.KindPermissionDenied, "caller is not signed by this helper's team")
}

// selfTeam returns this helper's own team identifier.
func selfTeam() (string, error) {
	buf := make([]byte, 64)
	n := int(C.cascade_self_team((*C.char)(unsafe.Pointer(&buf[0])), C.int(len(buf))))
	if n <= 0 || n >= len(buf) {
		return "", errCallerRefused()
	}
	return string(buf[:n]), nil
}

const tokenLen = 32

// peerToken reads the audit token of the peer of fd.
func peerToken(fd int) ([]byte, error) {
	tok := make([]byte, tokenLen)
	if C.cascade_peer_token(C.int(fd), (*C.uchar)(unsafe.Pointer(&tok[0]))) != 0 {
		return nil, errCallerRefused()
	}
	return tok, nil
}

// tokenSatisfies reports whether the process tok names satisfies requirement
// (and, when hardened is set, is running with the hardened runtime).
func tokenSatisfies(tok []byte, requirement string, hardened bool) bool {
	if len(tok) != tokenLen {
		return false
	}
	cReq := C.CString(requirement)
	defer C.free(unsafe.Pointer(cReq))
	h := C.int(0)
	if hardened {
		h = 1
	}
	return C.cascade_check_token((*C.uchar)(unsafe.Pointer(&tok[0])), cReq, h) == 0
}

// hasStorageEntitlement reports whether this helper can store a Keychain
// item with an access control at all.
func hasStorageEntitlement() bool { return C.cascade_has_storage_entitlement() == 1 }

// bindCaller returns the caller check for the connection whose descriptor is
// fd: it judges the process that owns the listener fd is connected to. The token is read once
// here, right after connecting, and again at every op: the two must be byte
// identical (same pid, same pid version) and both must satisfy the
// requirement, so an exec or a different writer in between is refused.
func bindCaller(fd int) (func() error, error) {
	first, err := peerToken(fd)
	if err != nil {
		return nil, err
	}
	return func() error { return checkCaller(fd, first, selfTeam, peerToken, tokenSatisfies) }, nil
}

// checkCaller is the caller check with its platform calls injected.
func checkCaller(fd int, first []byte, team func() (string, error), token func(fd int) ([]byte, error),
	satisfies func(tok []byte, requirement string, hardened bool) bool) error {
	t, err := team()
	if err != nil {
		return errCallerRefused()
	}
	req, err := elevation.CallerRequirement(t)
	if err != nil {
		return errCallerRefused()
	}
	now, err := token(fd)
	if err != nil || !bytes.Equal(first, now) || !satisfies(now, req, true) {
		return errCallerRefused()
	}
	return nil
}
