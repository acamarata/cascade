//go:build linux

// Purpose: the linux custody backend - the org.freedesktop.secrets session
//
//	secret service, spoken over D-Bus in pure Go.
//
// Inputs: a Config carrying the collection/service label; the session bus
//
//	address from the process environment (read by the D-Bus library).
//
// Outputs: a Custody backed by the running secret service (gnome-keyring,
//
//	KWallet's secret-service bridge, keepassxc, ...).
//
// Constraints: no libsecret and no CGO, so this backend survives the
//
//	CGO_ENABLED=0 release build. Every path fails closed: no session bus,
//	no secret service, or a locked collection is a typed KindUnavailable
//	refusal, and SelectCustody then falls back to the encrypted file vault
//	rather than to an empty store. No secret value is logged or placed in
//	an error message.
//
// SPORT: internal/secrets Custody/ADDED (linux secret service).

package secrets

import (
	"context"
	"errors"

	"github.com/godbus/dbus/v5"
)

const (
	linuxCustodyName = "secret-service"
	ssBusName        = "org.freedesktop.secrets"
	ssServicePath    = dbus.ObjectPath("/org/freedesktop/secrets")
	ssDefaultAlias   = dbus.ObjectPath("/org/freedesktop/secrets/aliases/default")
	ssServiceIface   = "org.freedesktop.Secret.Service"
	ssCollIface      = "org.freedesktop.Secret.Collection"
	ssItemIface      = "org.freedesktop.Secret.Item"
	// ssAttrService and ssAttrName are the lookup attributes every cascade
	// item carries, so a search matches this vault's entries and nothing
	// else on the user's session.
	ssAttrService = "cascade-vault-service"
	ssAttrName    = "cascade-vault-name"
)

// ssSecret mirrors the org.freedesktop.Secret.Item secret struct.
type ssSecret struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

// secretServiceCustody holds no live connection: each operation opens a
// session and closes it, so a vault operation never keeps a bus connection
// (and an unlocked session) alive between calls.
type secretServiceCustody struct {
	service string
	connect func() (*dbus.Conn, error)
}

// platformCustody builds the linux backend.
func platformCustody(cfg Config) (Custody, error) {
	return &secretServiceCustody{service: cfg.Service, connect: dbus.SessionBus}, nil
}

// platformElevatedRefusal reports the platform-wide refusal of elevated
// vault verbs. Linux is a tier-1 platform, so there is none.
func platformElevatedRefusal() error { return nil }

// Name reports the backend label used in diagnostics.
func (s *secretServiceCustody) Name() string { return linuxCustodyName }

// Available reports whether a session bus is reachable AND the secret
// service is actually claimed on it. Both are checked: a session bus with
// no secret service on it is a host where every later call would fail, and
// answering true there would mean selecting a backend that cannot store
// anything.
func (s *secretServiceCustody) Available() bool {
	conn, err := s.connect()
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()
	var owner string
	err = conn.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, ssBusName).Store(&owner)
	return err == nil && owner != ""
}

// ssSession is one open secret-service session plus its connection.
type ssSession struct {
	conn *dbus.Conn
	path dbus.ObjectPath
}

func (s *ssSession) close() {
	if s.path != "" {
		_ = s.conn.Object(ssBusName, s.path).Call("org.freedesktop.Secret.Session.Close", 0).Err
	}
	_ = s.conn.Close()
}

// open dials the session bus, opens a plain session and unlocks the default
// collection. Any failure is a typed refusal; nothing here proceeds on a
// half-open session.
func (s *secretServiceCustody) open() (*ssSession, error) {
	conn, err := s.connect()
	if err != nil {
		return nil, ErrCustodyUnavailable(linuxCustodyName, err)
	}
	var discard dbus.Variant
	var sessionPath dbus.ObjectPath
	svc := conn.Object(ssBusName, ssServicePath)
	if err := svc.Call(ssServiceIface+".OpenSession", 0, "plain", dbus.MakeVariant("")).
		Store(&discard, &sessionPath); err != nil {
		_ = conn.Close()
		return nil, ErrCustodyUnavailable(linuxCustodyName, err)
	}
	sess := &ssSession{conn: conn, path: sessionPath}
	if err := s.unlock(sess); err != nil {
		sess.close()
		return nil, err
	}
	return sess, nil
}

// unlock asks the service to unlock the default collection. A collection
// that comes back still locked (because unlocking needs an interactive
// prompt this non-interactive path will not drive) is refused rather than
// used, since every later read would fail with a less legible error.
func (s *secretServiceCustody) unlock(sess *ssSession) error {
	var unlocked []dbus.ObjectPath
	var prompt dbus.ObjectPath
	svc := sess.conn.Object(ssBusName, ssServicePath)
	if err := svc.Call(ssServiceIface+".Unlock", 0, []dbus.ObjectPath{ssDefaultAlias}).
		Store(&unlocked, &prompt); err != nil {
		return ErrCustodyUnavailable(linuxCustodyName, err)
	}
	if len(unlocked) == 0 {
		return ErrCustodyUnavailable(linuxCustodyName,
			errors.New("the default secret collection is locked and unlocking it needs an interactive prompt"))
	}
	return nil
}

func (s *secretServiceCustody) attrs(name string) map[string]string {
	return map[string]string{ssAttrService: s.service, ssAttrName: name}
}

// Set creates or replaces the item for name.
func (s *secretServiceCustody) Set(_ context.Context, name string, value []byte) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	sess, err := s.open()
	if err != nil {
		return err
	}
	defer sess.close()
	props := map[string]dbus.Variant{
		ssItemIface + ".Label":      dbus.MakeVariant("cascade vault: " + name),
		ssItemIface + ".Attributes": dbus.MakeVariant(s.attrs(name)),
	}
	secret := ssSecret{Session: sess.path, Value: value, ContentType: "application/octet-stream"}
	var item, prompt dbus.ObjectPath
	coll := sess.conn.Object(ssBusName, ssDefaultAlias)
	if err := coll.Call(ssCollIface+".CreateItem", 0, props, secret, true).Store(&item, &prompt); err != nil {
		return ErrCustodyUnavailable(linuxCustodyName, err)
	}
	return nil
}

// search returns the unlocked item paths matching attrs.
func (s *secretServiceCustody) search(sess *ssSession, attrs map[string]string) ([]dbus.ObjectPath, error) {
	var unlocked, locked []dbus.ObjectPath
	svc := sess.conn.Object(ssBusName, ssServicePath)
	if err := svc.Call(ssServiceIface+".SearchItems", 0, attrs).Store(&unlocked, &locked); err != nil {
		return nil, ErrCustodyUnavailable(linuxCustodyName, err)
	}
	if len(unlocked) == 0 && len(locked) > 0 {
		return nil, ErrCustodyUnavailable(linuxCustodyName,
			errors.New("the matching secret-service items are locked"))
	}
	return unlocked, nil
}

// Get reads one item's secret.
func (s *secretServiceCustody) Get(_ context.Context, name string) ([]byte, error) {
	if err := validateSecretName(name); err != nil {
		return nil, err
	}
	sess, err := s.open()
	if err != nil {
		return nil, err
	}
	defer sess.close()
	items, err := s.search(sess, s.attrs(name))
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrSecretNotFound(name)
	}
	var secret ssSecret
	if err := sess.conn.Object(ssBusName, items[0]).
		Call(ssItemIface+".GetSecret", 0, sess.path).Store(&secret); err != nil {
		return nil, ErrCustodyUnavailable(linuxCustodyName, err)
	}
	return secret.Value, nil
}

// Delete removes the item for name.
func (s *secretServiceCustody) Delete(_ context.Context, name string) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	sess, err := s.open()
	if err != nil {
		return err
	}
	defer sess.close()
	items, err := s.search(sess, s.attrs(name))
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return ErrSecretNotFound(name)
	}
	var prompt dbus.ObjectPath
	if err := sess.conn.Object(ssBusName, items[0]).Call(ssItemIface+".Delete", 0).Store(&prompt); err != nil {
		return ErrCustodyUnavailable(linuxCustodyName, err)
	}
	return nil
}

// List returns this vault's names, read from each item's attributes. No
// item secret is fetched: listing never touches a value.
func (s *secretServiceCustody) List(_ context.Context) ([]string, error) {
	sess, err := s.open()
	if err != nil {
		return nil, err
	}
	defer sess.close()
	items, err := s.search(sess, map[string]string{ssAttrService: s.service})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		prop, perr := sess.conn.Object(ssBusName, item).GetProperty(ssItemIface + ".Attributes")
		if perr != nil {
			return nil, ErrCustodyUnavailable(linuxCustodyName, perr)
		}
		attrs, ok := prop.Value().(map[string]string)
		if !ok {
			return nil, ErrCustodyCorrupt(linuxCustodyName, errors.New("an item's attributes are not a string map"))
		}
		if name := attrs[ssAttrName]; name != "" {
			names = append(names, name)
		}
	}
	return sortedNames(names), nil
}
