// Package wiregen generates wire-format code from Go struct definitions.
package wiregen

import (
	"errors"
	"reflect"
)

// ErrNotStruct is returned when a non-struct value is passed to Generate.
var ErrNotStruct = errors.New("wiregen: not a struct")

// ErrInvalidStruct is returned when an invalid struct is passed to Generate.
var ErrInvalidStruct = errors.New("wiregen: invalid struct")

// Generate produces wire-format text from the given struct values.
// Nil or empty argument list returns ("", nil).
// Non-struct values (including nil, pointers, and other kinds) return ("", ErrNotStruct).
// Only non-pointer values of named Go struct types are accepted.
func Generate(structs ...any) (string, error) {
	if len(structs) == 0 {
		return "", nil
	}

	for _, s := range structs {
		if s == nil {
			return "", ErrNotStruct
		}

		v := reflect.ValueOf(s)
		if v.Kind() != reflect.Struct {
			return "", ErrNotStruct
		}

		t := v.Type()
		if t.Name() == "" {
			return "", ErrInvalidStruct
		}
	}

	return "", nil
}
