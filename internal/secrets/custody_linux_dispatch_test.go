//go:build linux

// Purpose: the method-call handlers for the fake secret-service peer in
//
//	custody_linux_fake_test.go, split out to keep both files under the
//	engineering standard's line cap.
//
// Inputs: one decoded D-Bus method-call Message per call.
// Outputs: the Message to send back, a method reply or a typed error.
// Constraints: covers exactly the calls custody_linux.go makes; anything
//
//	else replies UnknownMethod rather than hanging the caller.
//
// SPORT: internal/secrets Custody/TEST (linux secret-service fake peer).

package secrets

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

// dispatch routes one method call to its handler under the service lock,
// since two backend calls (e.g. two Get()s in parallel) may race here even
// though each opens its own session.
func (f *fakeSecretService) dispatch(msg *dbus.Message) *dbus.Message {
	f.mu.Lock()
	defer f.mu.Unlock()

	iface, _ := msg.Headers[dbus.FieldInterface].Value().(string)
	member, _ := msg.Headers[dbus.FieldMember].Value().(string)
	path, _ := msg.Headers[dbus.FieldPath].Value().(dbus.ObjectPath)

	switch {
	case member == "GetNameOwner":
		return f.handleGetNameOwner(msg)
	case iface == ssServiceIface && member == "OpenSession":
		return f.handleOpenSession(msg)
	case iface == ssServiceIface && member == "Unlock":
		return f.handleUnlock(msg)
	case iface == ssServiceIface && member == "SearchItems":
		return f.handleSearchItems(msg)
	case iface == ssCollIface && member == "CreateItem":
		return f.handleCreateItem(msg)
	case iface == ssItemIface && member == "GetSecret":
		return f.handleGetSecret(msg, path)
	case iface == ssItemIface && member == "Delete":
		return f.handleDeleteItem(msg, path)
	case iface == "org.freedesktop.DBus.Properties" && member == "Get":
		return f.handlePropertiesGet(msg, path)
	case iface == "org.freedesktop.Secret.Session" && member == "Close":
		return okReply(msg)
	default:
		return errReply("org.freedesktop.DBus.Error.UnknownMethod")
	}
}

func (f *fakeSecretService) handleGetNameOwner(msg *dbus.Message) *dbus.Message {
	if f.unowned {
		return errReply("org.freedesktop.DBus.Error.NameHasNoOwner")
	}
	return okReply(msg, "cascade-test-owner")
}

func (f *fakeSecretService) handleOpenSession(msg *dbus.Message) *dbus.Message {
	if f.failOpenSession {
		return errReply("org.freedesktop.DBus.Error.Failed")
	}
	return okReply(msg, dbus.MakeVariant(""), dbus.ObjectPath("/org/freedesktop/secrets/session/s1"))
}

func (f *fakeSecretService) handleUnlock(msg *dbus.Message) *dbus.Message {
	var unlocked []dbus.ObjectPath
	if !f.locked {
		unlocked, _ = msg.Body[0].([]dbus.ObjectPath)
	}
	return okReply(msg, unlocked, dbus.ObjectPath("/"))
}

func (f *fakeSecretService) handleSearchItems(msg *dbus.Message) *dbus.Message {
	if f.failSearch {
		return errReply("org.freedesktop.DBus.Error.Failed")
	}
	want, _ := msg.Body[0].(map[string]string)
	var unlocked, locked []dbus.ObjectPath
	for _, it := range f.items {
		if !attrsMatch(it.attrs, want) {
			continue
		}
		if it.locked {
			locked = append(locked, it.path)
		} else {
			unlocked = append(unlocked, it.path)
		}
	}
	return okReply(msg, unlocked, locked)
}

func (f *fakeSecretService) handleCreateItem(msg *dbus.Message) *dbus.Message {
	if f.failCreate {
		return errReply("org.freedesktop.DBus.Error.Failed")
	}
	props, _ := msg.Body[0].(map[string]dbus.Variant)
	attrs, _ := props[ssItemIface+".Attributes"].Value().(map[string]string)
	fields, _ := msg.Body[1].([]any)
	var value []byte
	if len(fields) >= 3 {
		value, _ = fields[2].([]byte)
	}
	f.nextID++
	p := dbus.ObjectPath(fmt.Sprintf("/org/freedesktop/secrets/collection/login/i%d", f.nextID))
	f.items = append(f.items, &fakeSecretItem{path: p, attrs: attrs, value: value})
	return okReply(msg, p, dbus.ObjectPath("/"))
}

func (f *fakeSecretService) handleGetSecret(msg *dbus.Message, path dbus.ObjectPath) *dbus.Message {
	if f.failGetSecret {
		return errReply("org.freedesktop.DBus.Error.Failed")
	}
	it := f.itemAt(path)
	if it == nil {
		return errReply("org.freedesktop.DBus.Error.UnknownObject")
	}
	// Session must be a syntactically valid object path even though
	// production code never reads it back; the wire encoder rejects an
	// empty ObjectPath outright.
	return okReply(msg, ssSecret{
		Session:     "/org/freedesktop/secrets/session/s1",
		Value:       it.value,
		ContentType: "application/octet-stream",
	})
}

func (f *fakeSecretService) handleDeleteItem(msg *dbus.Message, path dbus.ObjectPath) *dbus.Message {
	if f.failDelete {
		return errReply("org.freedesktop.DBus.Error.Failed")
	}
	f.removeItem(path)
	return okReply(msg, dbus.ObjectPath("/"))
}

func (f *fakeSecretService) handlePropertiesGet(msg *dbus.Message, path dbus.ObjectPath) *dbus.Message {
	if f.failPropGet {
		return errReply("org.freedesktop.DBus.Error.Failed")
	}
	it := f.itemAt(path)
	if it == nil {
		return errReply("org.freedesktop.DBus.Error.UnknownObject")
	}
	if f.corruptAttrs {
		return okReply(msg, dbus.MakeVariant("not-a-map"))
	}
	return okReply(msg, dbus.MakeVariant(it.attrs))
}

// okReply builds a method-reply Message; body is optional, matching the
// zero-return calls (Session.Close) production code makes.
func okReply(_ *dbus.Message, body ...any) *dbus.Message {
	reply := &dbus.Message{Type: dbus.TypeMethodReply, Headers: map[dbus.HeaderField]dbus.Variant{}}
	if len(body) > 0 {
		reply.Body = body
		reply.Headers[dbus.FieldSignature] = dbus.MakeVariant(dbus.SignatureOf(body...))
	}
	return reply
}

// errReply builds a D-Bus error reply carrying name; ReplySerial is filled
// in by the caller once the request's serial is known.
func errReply(name string) *dbus.Message {
	return &dbus.Message{
		Type:    dbus.TypeError,
		Headers: map[dbus.HeaderField]dbus.Variant{dbus.FieldErrorName: dbus.MakeVariant(name)},
	}
}
