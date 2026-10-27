// Purpose: remembers the scrypt output the file vault derives for a vault
// file, so reading an unchanged file does not repeat the key derivation.
// Inputs: passphrase, the salt and work factor from the file header.
// Outputs: the 32-byte wrap key, identical to ageScryptKey's.
// Constraints: scrypt at the file work factor costs tens of milliseconds
// (several hundred under the race detector) and every audit record reads the
// vault, so a single grant paid it a dozen times. The memo holds only derived
// wrap keys, never vault contents; the header MAC and chunk authentication
// still run on every read, so a changed or forged file is still refused. It
// is bounded and used by the file vault only, never for a user-supplied
// export passphrase.
// SPORT: secrets.filevault.keymemo (CHANGE).

package secrets

import (
	"crypto/sha256"
	"strconv"
	"sync"
)

// memoScryptMax bounds the memo. A process opens a handful of vault files
// (the daemon's, plus any tests' distinct ones); a save writes a fresh salt,
// so older entries are dropped oldest-first.
const memoScryptMax = 16

var scryptMemo struct {
	mu    sync.Mutex
	keys  map[[sha256.Size]byte][]byte
	order [][sha256.Size]byte
}

// memoScryptKey returns ageScryptKey(passphrase, salt, logN), computing it
// once per distinct input. The memo key is a hash of the inputs, so the
// passphrase is not kept as a map key.
func memoScryptKey(passphrase string, salt []byte, logN int) ([]byte, error) {
	h := sha256.New()
	h.Write([]byte(strconv.Itoa(len(passphrase)) + ":" + passphrase))
	h.Write([]byte(strconv.Itoa(logN) + ":"))
	h.Write(salt)
	var id [sha256.Size]byte
	copy(id[:], h.Sum(nil))

	scryptMemo.mu.Lock()
	if key, ok := scryptMemo.keys[id]; ok {
		scryptMemo.mu.Unlock()
		return append([]byte(nil), key...), nil
	}
	scryptMemo.mu.Unlock()

	key, err := ageScryptKey(passphrase, salt, logN)
	if err != nil {
		return nil, err
	}
	scryptMemo.mu.Lock()
	defer scryptMemo.mu.Unlock()
	if scryptMemo.keys == nil {
		scryptMemo.keys = make(map[[sha256.Size]byte][]byte, memoScryptMax)
	}
	if _, ok := scryptMemo.keys[id]; !ok {
		if len(scryptMemo.order) >= memoScryptMax {
			delete(scryptMemo.keys, scryptMemo.order[0])
			scryptMemo.order = scryptMemo.order[1:]
		}
		scryptMemo.keys[id] = append([]byte(nil), key...)
		scryptMemo.order = append(scryptMemo.order, id)
	}
	return key, nil
}
