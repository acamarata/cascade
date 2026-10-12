package service

// Purpose: test-only reader that recovers argv and environment from the
//   plist the installer wrote, so install tests can assert on them. It is
//   never used for trust decisions (status compares bytes).
// SPORT: internal/daemon/service (P1-WID-04).

import (
	"bytes"
	"encoding/xml"
	"io"
)

// plistValue is one top-level plist value: its element kind (string, true,
// false, array, dict, integer ...) and, for a string, its text.
type plistValue struct {
	Kind string
	Str  string
}

// widgetPlist is what status needs back out of an installed plist: the
// recovered argv and environment plus every top-level key with its value
// type, for test assertions.
type widgetPlist struct {
	ProgramArguments []string
	Environment      map[string]string
	Top              map[string]plistValue
	// Odd is set for anything the installer never writes: a duplicate key, a
	// non-string argv or environment value, or a nested container.
	Odd bool
}

// plistReader is the parse state while walking the token stream.
type plistReader struct {
	out     widgetPlist
	depth   int
	key     string // current top-level key
	envKey  string // current key inside EnvironmentVariables
	section string // "ProgramArguments" or "EnvironmentVariables" while inside it
}

// parseWidgetPlist reads the whole top-level dict from plist XML. A
// malformed document is an error.
func parseWidgetPlist(data []byte) (widgetPlist, error) {
	r := &plistReader{out: widgetPlist{Environment: map[string]string{}, Top: map[string]plistValue{}}}
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return r.out, nil
		}
		if err != nil {
			return widgetPlist{}, err
		}
		switch t := tok.(type) {
		case xml.EndElement:
			if t.Name.Local == "dict" || t.Name.Local == "array" {
				r.depth--
				if r.depth == 1 {
					r.section = ""
				}
			}
		case xml.StartElement:
			if err := r.start(dec, t); err != nil {
				return widgetPlist{}, err
			}
		}
	}
}

// start handles one start element.
func (r *plistReader) start(dec *xml.Decoder, se xml.StartElement) error {
	name := se.Name.Local
	switch name {
	case "plist":
		return nil
	case "key":
		var k string
		if err := dec.DecodeElement(&k, &se); err != nil {
			return err
		}
		switch r.depth {
		case 1:
			r.key = k
		case 2:
			r.envKey = k
		}
		return nil
	case "dict", "array":
		r.container(name)
		return nil
	}
	var v string
	if name == "string" {
		if err := dec.DecodeElement(&v, &se); err != nil {
			return err
		}
	}
	r.scalar(name, v)
	return nil
}

// container records a dict or array start and enters it.
func (r *plistReader) container(kind string) {
	switch {
	case r.depth == 1:
		r.setTop(kind, "")
		if r.key == "EnvironmentVariables" || r.key == "ProgramArguments" {
			r.section = r.key
		}
	case r.depth >= 2:
		r.out.Odd = true
	}
	r.depth++
}

// scalar records a non-container value in the current position.
func (r *plistReader) scalar(kind, v string) {
	switch {
	case r.depth == 1:
		r.setTop(kind, v)
	case r.section == "ProgramArguments" && kind == "string":
		r.out.ProgramArguments = append(r.out.ProgramArguments, v)
	case r.section == "EnvironmentVariables" && kind == "string":
		if _, dup := r.out.Environment[r.envKey]; dup {
			r.out.Odd = true
		}
		r.out.Environment[r.envKey] = v
	default:
		r.out.Odd = true
	}
}

// setTop stores the value of the current top-level key; a repeat is Odd.
func (r *plistReader) setTop(kind, v string) {
	if _, dup := r.out.Top[r.key]; dup {
		r.out.Odd = true
	}
	r.out.Top[r.key] = plistValue{Kind: kind, Str: v}
}
