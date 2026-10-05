package a

import "io/ioutil"

// Save writes state with the deprecated bare write.
func Save(p string) error {
	return ioutil.WriteFile(p, nil, 0o600)
}
