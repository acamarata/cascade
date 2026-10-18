//go:build darwin && cgo

// Purpose: the two darwin keystore operations only the elevation helper
//
//	needs, kept apart from the in-process keystore: signing with a prompt
//	text the helper builds from the payload digest, and deleting the
//	enrolled key behind a device-owner presence check.
//
// Inputs: the signing payload and prompt text, or the delete prompt text.
// Outputs: SignWithReason returns the Ed25519 signature; DevicePresence gates
//
//	DeletePlatformKeyItems, which removes both Keychain items.
//
// Constraints: the presence gate (DevicePresence) and the delete are separate
//
//	so the helper's protocol layer owns, and tests, the order (a refused
//	presence deletes nothing); the delete is a distinct code path from
//	GenerateKey, which never replaces an existing key. Only
//	cmd/cascade-elevate-helper calls these functions.
//
// SPORT: internal/elevation DeletePlatformKeyItems/ADDED (P1-SHIP-15).

package elevation

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework LocalAuthentication -framework Security

#import <Foundation/Foundation.h>
#import <LocalAuthentication/LocalAuthentication.h>
#include <Security/Security.h>

// cascade_delete_presence evaluates device-owner authentication with the
// given reason. Returns 1 on success, 0 when authentication failed or was
// canceled, -1 when the policy cannot be evaluated on this host.
static int cascade_delete_presence(const char *reason) {
    LAContext *ctx = [[LAContext alloc] init];
    NSError *policyErr = nil;
    if (![ctx canEvaluatePolicy:LAPolicyDeviceOwnerAuthentication error:&policyErr]) {
        return -1;
    }
    __block BOOL authed = NO;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    [ctx evaluatePolicy:LAPolicyDeviceOwnerAuthentication
        localizedReason:[NSString stringWithUTF8String:reason]
                  reply:^(BOOL success, NSError *evalErr) {
        authed = success;
        dispatch_semaphore_signal(sem);
    }];
    // Bounded a little under the caller's 60 s kill: an abandoned prompt
    // must not stay up and later approve a delete nobody is waiting for.
    if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 55 * NSEC_PER_SEC)) != 0) {
        [ctx invalidate];
        return 0;
    }
    return authed ? 1 : 0;
}

// cascade_item_probe reports whether a generic-password item exists without
// reading its data or ever showing UI: 0 exists, 1 definitely absent, -1 the
// state cannot be told (any other status), so the caller must refuse.
static int cascade_item_probe(const char *service, const char *account) {
    LAContext *ctx = [[LAContext alloc] init];
    ctx.interactionNotAllowed = YES;
    NSMutableDictionary *q = [NSMutableDictionary dictionary];
    q[(__bridge id)kSecClass] = (__bridge id)kSecClassGenericPassword;
    q[(__bridge id)kSecAttrService] = [NSString stringWithUTF8String:service];
    q[(__bridge id)kSecAttrAccount] = [NSString stringWithUTF8String:account];
    q[(__bridge id)kSecReturnAttributes] = @YES;
    q[(__bridge id)kSecMatchLimit] = (__bridge id)kSecMatchLimitOne;
    q[(__bridge id)kSecUseAuthenticationContext] = ctx;
    OSStatus st = SecItemCopyMatching((__bridge CFDictionaryRef)q, NULL);
    if (st == errSecSuccess || st == errSecInteractionNotAllowed || st == errSecAuthFailed) return 0;
    if (st == errSecItemNotFound) return 1;
    return -1;
}

// cascade_item_add_new stores a generic-password item and never replaces one:
// an existing item is errSecDuplicateItem. protect!=0 adds the same
// device-owner-presence access control the in-process keystore uses.
static OSStatus cascade_item_add_new(const char *service, const char *account,
                                     const unsigned char *data, int len, int protect) {
    NSMutableDictionary *add = [NSMutableDictionary dictionary];
    add[(__bridge id)kSecClass] = (__bridge id)kSecClassGenericPassword;
    add[(__bridge id)kSecAttrService] = [NSString stringWithUTF8String:service];
    add[(__bridge id)kSecAttrAccount] = [NSString stringWithUTF8String:account];
    add[(__bridge id)kSecValueData] = [NSData dataWithBytes:data length:len];
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

// cascade_item_delete removes one generic-password item. Returns an OSStatus.
static OSStatus cascade_item_delete(const char *service, const char *account) {
    NSMutableDictionary *q = [NSMutableDictionary dictionary];
    q[(__bridge id)kSecClass] = (__bridge id)kSecClassGenericPassword;
    q[(__bridge id)kSecAttrService] = [NSString stringWithUTF8String:service];
    q[(__bridge id)kSecAttrAccount] = [NSString stringWithUTF8String:account];
    return SecItemDelete((__bridge CFDictionaryRef)q);
}
*/
import "C"

import (
	"crypto/ed25519"
	"unsafe"

	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	errSecItemNotFound  = -25300
	errSecDuplicateItem = -25299
)

// reasonBridge is the real bridge with the sign prompt text replaced.
type reasonBridge struct {
	darwinBridge
	reason string
}

func (b reasonBridge) signingKeyLoad(_ string, capacity int) ([]byte, int) {
	return b.darwinBridge.signingKeyLoad(b.reason, capacity)
}

// SignWithReason is darwinKeystore.Sign with the LocalAuthentication prompt
// text supplied by the caller (the helper derives it from the payload
// digest; no remote caller text ever reaches here).
func SignWithReason(payload []byte, reason string) ([]byte, error) {
	return darwinKeystore{bridge: reasonBridge{darwinBridge: cgoBridge{}, reason: reason}}.Sign(payload)
}

// DevicePresence evaluates device-owner authentication with reason. It is
// the presence gate in front of DeletePlatformKeyItems; the helper's delete
// op never calls the latter before this returns nil.
func DevicePresence(reason string) error {
	cReason := C.CString(reason)
	defer C.free(unsafe.Pointer(cReason))
	switch C.cascade_delete_presence(cReason) {
	case 1:
		return nil
	case 0:
		return ErrAuthFailed(nil)
	}
	return ErrKeystoreUnavailable(nil)
}

// DeletePlatformKeyItems removes the enrolled private and public Keychain
// items. It does not authenticate: the caller must have passed
// DevicePresence first. A key that was never enrolled is ErrHelperNotEnrolled.
func DeletePlatformKeyItems() error {
	// Public item first: a kill between the two leaves a private item with no
	// public one, which EnrollmentAbsent still reports as enrolled.
	pub, priv := deleteItem(darwinPubAcct), deleteItem(darwinPrivAcct)
	if priv == errSecItemNotFound && pub == errSecItemNotFound {
		return ErrHelperNotEnrolled()
	}
	for _, status := range []int{priv, pub} {
		if status != 0 && status != errSecItemNotFound {
			return cascade.Newf(cascade.KindUnavailable, "elevation: delete Keychain item (OSStatus %d)", status)
		}
	}
	return nil
}

func deleteItem(account string) int {
	cSvc, cAcct := C.CString(darwinService), C.CString(account)
	defer C.free(unsafe.Pointer(cSvc))
	defer C.free(unsafe.Pointer(cAcct))
	return int(C.cascade_item_delete(cSvc, cAcct))
}

// EnrollmentAbsent reports nil only when BOTH Keychain items are
// definitely absent. Present items, or any state it cannot tell, are errors:
// the helper's generate op must fail closed, because the store path replaces
// an existing item without any presence check.
func EnrollmentAbsent() error {
	for _, account := range []string{darwinPubAcct, darwinPrivAcct} {
		cSvc, cAcct := C.CString(darwinService), C.CString(account)
		state := int(C.cascade_item_probe(cSvc, cAcct))
		C.free(unsafe.Pointer(cSvc))
		C.free(unsafe.Pointer(cAcct))
		switch state {
		case 1:
		case 0:
			return ErrAlreadyEnrolled()
		default:
			return ErrKeystoreUnavailable(nil)
		}
	}
	return nil
}

func addNew(account string, data []byte, protect bool) int {
	cSvc, cAcct := C.CString(darwinService), C.CString(account)
	defer C.free(unsafe.Pointer(cSvc))
	defer C.free(unsafe.Pointer(cAcct))
	p := C.int(0)
	if protect {
		p = 1
	}
	return int(C.cascade_item_add_new(cSvc, cAcct, (*C.uchar)(unsafe.Pointer(&data[0])), C.int(len(data)), p))
}

// GenerateKeyNoReplace enrolls a fresh Ed25519 key and never replaces an
// existing Keychain item: the in-process GenerateKey deletes before it adds,
// so two concurrent enrollments could replace each other's key with no
// presence check. Here the first add wins and the other gets
// ErrAlreadyEnrolled; no failure path ever deletes an item.
func GenerateKeyNoReplace() error {
	pub, priv, err := ed25519.GenerateKey(edKeyRandSource)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "elevation: generate ed25519 key")
	}
	defer zero(priv)
	switch st := addNew(darwinPrivAcct, priv, true); st {
	case 0:
	case errSecDuplicateItem:
		return ErrAlreadyEnrolled()
	default:
		return cascade.Newf(cascade.KindUnavailable, "elevation: store private key in Keychain (OSStatus %d)", st)
	}
	if st := addNew(darwinPubAcct, pub, false); st != 0 {
		// The private item stays: deleting it by account name could destroy a
		// newer enrollment's key, and an orphan private item only makes
		// generate refuse until the owner deletes it with presence.
		if st == errSecDuplicateItem {
			return ErrAlreadyEnrolled()
		}
		return cascade.Newf(cascade.KindUnavailable, "elevation: store public key in Keychain (OSStatus %d)", st)
	}
	return nil
}
