// Purpose: the age v1 (scrypt recipient) decrypt half of the file-vault
//
//	codec. Split from agefmt.go for the 300-line file cap; the two halves
//	are one unit and share agefmt.go's constants.
//
// Inputs: a passphrase and an age v1 file.
// Outputs: the recovered plaintext, or a parse/authentication failure.
// Constraints: fails closed. A file this parser cannot fully authenticate
//
//	is refused; there is no partial read, no "best effort" plaintext, and
//	no error message that echoes file bytes or the passphrase.
//
// SPORT: internal/secrets file-vault-age-codec/ADDED.

package secrets

import (
	"bytes"
	"crypto/hmac"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

// ageDecrypt recovers the plaintext of an age v1 scrypt-recipient file.
// Any deviation from the format, an unrecognised recipient stanza, a
// failed header MAC or a failed chunk authentication is an error: this
// never returns a partial or unauthenticated plaintext.
func ageDecrypt(passphrase string, file []byte) ([]byte, error) {
	if passphrase == "" {
		return nil, errors.New("age: refusing to decrypt with an empty passphrase")
	}
	hdr, body, err := ageSplit(file)
	if err != nil {
		return nil, err
	}
	salt, logN, wrapped, err := ageParseHeader(hdr.lines)
	if err != nil {
		return nil, err
	}
	wrapKey, err := ageScryptKey(passphrase, salt, logN)
	if err != nil {
		return nil, err
	}
	fileKey, err := ageOpenFileKey(wrapKey, wrapped)
	if err != nil {
		return nil, err
	}
	mac, err := ageHeaderMAC(fileKey, hdr.macCovered)
	if err != nil {
		return nil, err
	}
	if !hmac.Equal(mac, hdr.mac) {
		return nil, fmt.Errorf("%w: header authentication failed", errAgeFormat)
	}
	if len(body) < 16 {
		return nil, fmt.Errorf("%w: payload is truncated", errAgeFormat)
	}
	return ageOpenPayload(fileKey, body[:16], body[16:])
}

// ageHeader is a parsed header: the recipient/intro lines, the exact bytes
// the MAC covers, and the MAC itself.
type ageHeader struct {
	lines      []string
	macCovered []byte
	mac        []byte
}

// ageSplit locates the "---" footer line, returning the header (with the
// MAC-covered span) and the binary body that follows it.
func ageSplit(file []byte) (ageHeader, []byte, error) {
	marker := []byte("\n" + ageFooter + " ")
	idx := bytes.Index(file, marker)
	if idx < 0 {
		return ageHeader{}, nil, fmt.Errorf("%w: no header footer", errAgeFormat)
	}
	covered := file[:idx+1+len(ageFooter)]
	rest := file[idx+len(marker):]
	nl := bytes.IndexByte(rest, '\n')
	if nl < 0 {
		return ageHeader{}, nil, fmt.Errorf("%w: header footer is unterminated", errAgeFormat)
	}
	mac, err := ageB64.DecodeString(string(rest[:nl]))
	if err != nil {
		return ageHeader{}, nil, fmt.Errorf("%w: header mac is not valid base64", errAgeFormat)
	}
	lines := strings.Split(string(file[:idx]), "\n")
	return ageHeader{lines: lines, macCovered: covered, mac: mac}, rest[nl+1:], nil
}

// ageParseHeader reads the intro line and the single scrypt stanza. A file
// carrying any other recipient type is refused rather than searched for a
// stanza this build happens to understand.
func ageParseHeader(lines []string) (salt []byte, logN int, wrapped []byte, err error) {
	if len(lines) != 3 || lines[0] != ageIntro {
		return nil, 0, nil, fmt.Errorf("%w: unsupported header shape", errAgeFormat)
	}
	fields := strings.Fields(lines[1])
	if len(fields) != 4 || fields[0] != "->" || fields[1] != ageScryptTag {
		return nil, 0, nil, fmt.Errorf("%w: only the scrypt recipient is supported", errAgeFormat)
	}
	if salt, err = ageB64.DecodeString(fields[2]); err != nil || len(salt) != 16 {
		return nil, 0, nil, fmt.Errorf("%w: bad scrypt salt", errAgeFormat)
	}
	if logN, err = strconv.Atoi(fields[3]); err != nil || logN < 1 || logN > ageMaxLogN {
		return nil, 0, nil, fmt.Errorf("%w: scrypt work factor out of range", errAgeFormat)
	}
	if wrapped, err = ageB64.DecodeString(lines[2]); err != nil || len(wrapped) != 32 {
		return nil, 0, nil, fmt.Errorf("%w: bad wrapped file key", errAgeFormat)
	}
	return salt, logN, wrapped, nil
}

func ageOpenFileKey(wrapKey, wrapped []byte) ([]byte, error) {
	a, err := chacha20poly1305.New(wrapKey)
	if err != nil {
		return nil, fmt.Errorf("age: aead: %w", err)
	}
	key, err := a.Open(nil, make([]byte, chacha20poly1305.NonceSize), wrapped, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: passphrase did not unwrap the file key", errAgeFormat)
	}
	return key, nil
}

// ageOpenPayload authenticates and decrypts the STREAM body. A body whose
// final chunk is not flagged final is rejected: that is the truncation
// attack the flag exists to stop.
func ageOpenPayload(fileKey, nonce, body []byte) ([]byte, error) {
	a, err := ageStreamAEAD(fileKey, nonce)
	if err != nil {
		return nil, err
	}
	const sealed = ageChunkSize + chacha20poly1305.Overhead
	var out []byte
	counter := uint64(0)
	for len(body) > 0 {
		take := min(len(body), sealed)
		// The final chunk is the one nothing follows, NOT merely a short
		// one: a plaintext that is an exact multiple of the chunk size
		// ends with a full-size chunk that still carries the final flag.
		last := take == len(body)
		chunk, openErr := a.Open(nil, ageStreamNonce(counter, last), body[:take], nil)
		if openErr != nil {
			return nil, fmt.Errorf("%w: payload authentication failed", errAgeFormat)
		}
		out = append(out, chunk...)
		body = body[take:]
		counter++
	}
	return out, nil
}
