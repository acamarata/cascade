package minisign

import (
	"encoding/base64"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestParsePublicKeyBareLine(t *testing.T) {
	for _, path := range []string{"testdata/minisign/test.pub", "../../docs/keys/minisign.pub"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want, err := ParsePublicKey(data)
		if err != nil {
			t.Fatal(err)
		}
		line := strings.Split(string(data), "\n")[1]
		for _, suffix := range []string{"", "\n"} {
			got, err := ParsePublicKey([]byte(line + suffix))
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("bare %s: %v", path, err)
			}
		}
	}
}

func TestParsePublicKeyBareLineMalformedRefused(t *testing.T) {
	valid := base64.StdEncoding.EncodeToString(append([]byte("Ed"), make([]byte, 40)...))
	for input, reason := range map[string]string{
		"!!!":  "line 2 is not valid base64",
		"AA==": "expected 42 decoded bytes",
		base64.StdEncoding.EncodeToString(append([]byte("XX"), make([]byte, 40)...)): "unrecognized algorithm tag XX",
		valid + " trailing":             "line 2 is not valid base64",
		"comment\n" + valid + "\nextra": "expected exactly 2 lines",
	} {
		_, err := ParsePublicKey([]byte(input))
		want := cascade.New(cascade.KindInvalidInput, "nodes: minisign signature malformed: public key: "+reason)
		if !errors.Is(err, want) || err.Error() != want.Error() {
			t.Fatalf("got %v, want %v", err, want)
		}
	}
}
