// Purpose: a minimal reader/writer for the age v1 encrypted-file format,
//
//	scrypt (passphrase) recipient only, used by the encrypted file-vault
//	custody backend.
//
// Inputs: a passphrase and a plaintext (encrypt) or an age file (decrypt).
// Outputs: an age v1 file byte slice, or the recovered plaintext.
// Constraints: the on-disk format is the real age v1 format, not a
//
//	look-alike, so an operator can decrypt a cascade file vault with the
//	stock `age` tool and the project's golden fixture is a file the real
//	tool produced. Pure Go, no CGO. Nothing here logs, and no error
//	message carries plaintext, passphrase or key material.
//
// SPORT: internal/secrets file-vault-age-codec/ADDED.

package secrets

import (
	"bytes"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"
)

const (
	ageIntro     = "age-encryption.org/v1"
	ageScryptTag = "scrypt"
	ageScryptSSD = "age-encryption.org/v1/scrypt"
	ageFooter    = "---"
	// ageChunkSize is the age STREAM plaintext chunk size (64 KiB).
	ageChunkSize = 64 * 1024
	// ageDefaultLogN is the scrypt work factor for files this package
	// writes. age's own CLI default is 18; the vault passphrase is a
	// 256-bit random key file rather than a human password, so the work
	// factor guards only against an offline attacker who already holds
	// that file, and 15 keeps an unlock under a few hundred milliseconds.
	ageDefaultLogN = 15
	// ageMaxLogN bounds a work factor read from a file. Without it a
	// hostile file can pin a CPU for hours before failing to decrypt.
	ageMaxLogN = 20
)

// ageB64 is age's canonical unpadded standard base64.
var ageB64 = base64.RawStdEncoding

// errAgeFormat is the internal parse failure. Callers wrap it with the
// taxonomy kind that fits their layer.
var errAgeFormat = errors.New("age: malformed file")

// ageEncrypt produces an age v1 file holding plaintext, unlockable with
// passphrase. rnd supplies the scrypt salt, the file key and the payload
// nonce; tests inject a deterministic reader so a fixture is reproducible.
func ageEncrypt(passphrase string, plaintext []byte, logN int, rnd io.Reader) ([]byte, error) {
	if passphrase == "" {
		return nil, errors.New("age: refusing to encrypt with an empty passphrase")
	}
	if logN < 1 || logN > ageMaxLogN {
		return nil, fmt.Errorf("age: scrypt work factor %d out of range", logN)
	}
	salt := make([]byte, 16)
	fileKey := make([]byte, 16)
	nonce := make([]byte, 16)
	for _, b := range [][]byte{salt, fileKey, nonce} {
		if _, err := io.ReadFull(rnd, b); err != nil {
			return nil, fmt.Errorf("age: entropy source failed: %w", err)
		}
	}
	wrapKey, err := ageScryptKey(passphrase, salt, logN)
	if err != nil {
		return nil, err
	}
	wrapped, err := ageSealFileKey(wrapKey, fileKey)
	if err != nil {
		return nil, err
	}
	header := ageHeaderBytes(salt, logN, wrapped)
	mac, err := ageHeaderMAC(fileKey, header)
	if err != nil {
		return nil, err
	}
	body, err := ageSealPayload(fileKey, nonce, plaintext)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Write(header)
	out.WriteString(" " + ageB64.EncodeToString(mac) + "\n")
	out.Write(nonce)
	out.Write(body)
	return out.Bytes(), nil
}

// ageHeaderBytes renders the header up to and including the "---" marker
// (no trailing space or MAC), which is exactly the span the header MAC
// covers.
func ageHeaderBytes(salt []byte, logN int, wrapped []byte) []byte {
	var b bytes.Buffer
	b.WriteString(ageIntro + "\n")
	b.WriteString("-> " + ageScryptTag + " " + ageB64.EncodeToString(salt) + " " + strconv.Itoa(logN) + "\n")
	b.WriteString(ageB64.EncodeToString(wrapped) + "\n")
	b.WriteString(ageFooter)
	return b.Bytes()
}

func ageScryptKey(passphrase string, salt []byte, logN int) ([]byte, error) {
	full := append([]byte(ageScryptSSD), salt...)
	key, err := scrypt.Key([]byte(passphrase), full, 1<<logN, 8, 1, chacha20poly1305.KeySize)
	if err != nil {
		return nil, fmt.Errorf("age: scrypt: %w", err)
	}
	return key, nil
}

func ageSealFileKey(wrapKey, fileKey []byte) ([]byte, error) {
	aead, err := chacha20poly1305.New(wrapKey)
	if err != nil {
		return nil, fmt.Errorf("age: aead: %w", err)
	}
	return aead.Seal(nil, make([]byte, chacha20poly1305.NonceSize), fileKey, nil), nil
}

func ageHeaderMAC(fileKey, header []byte) ([]byte, error) {
	key, err := hkdf.Key(sha256.New, fileKey, nil, "header", sha256.Size)
	if err != nil {
		return nil, fmt.Errorf("age: hkdf: %w", err)
	}
	m := hmac.New(sha256.New, key)
	m.Write(header)
	return m.Sum(nil), nil
}

// ageSealPayload encrypts plaintext as an age STREAM body: 64 KiB chunks,
// each sealed under a 12-byte nonce of an 11-byte big-endian counter plus a
// final-chunk flag byte.
func ageSealPayload(fileKey, nonce, plaintext []byte) ([]byte, error) {
	aead, err := ageStreamAEAD(fileKey, nonce)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	counter := uint64(0)
	for i := 0; ; i += ageChunkSize {
		end := min(i+ageChunkSize, len(plaintext))
		last := end == len(plaintext)
		out.Write(aead.Seal(nil, ageStreamNonce(counter, last), plaintext[i:end], nil))
		if last {
			break
		}
		counter++
	}
	return out.Bytes(), nil
}

func ageStreamAEAD(fileKey, nonce []byte) (aead, error) {
	key, err := hkdf.Key(sha256.New, fileKey, nonce, "payload", chacha20poly1305.KeySize)
	if err != nil {
		return nil, fmt.Errorf("age: hkdf: %w", err)
	}
	a, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, fmt.Errorf("age: aead: %w", err)
	}
	return a, nil
}

// aead is the subset of cipher.AEAD this file uses.
type aead interface {
	Seal(dst, nonce, plaintext, additionalData []byte) []byte
	Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
}

func ageStreamNonce(counter uint64, last bool) []byte {
	n := make([]byte, chacha20poly1305.NonceSize)
	for i := 0; i < 8; i++ {
		n[10-i] = byte(counter >> (8 * i))
	}
	if last {
		n[11] = 1
	}
	return n
}
