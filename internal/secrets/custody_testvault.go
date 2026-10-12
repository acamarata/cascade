// Purpose: the QA custody switch. A development build (the devkeys tag) run
//
//	with CASCADE_TEST_FILE_VAULT=1 keeps its process vault in the encrypted
//	file vault under the cascade home, so a real-binary QA run never reads
//	or writes the login keychain or the secret service.
//
// Inputs: whether this is a devkeys build, decided by the composition root
//
//	from the one devkeys constant (internal/elevation), and an environment
//	reader. This package never imports internal/elevation: the vault and the
//	elevation keystore stay unable to reach each other.
//
// Outputs: whether the file vault is forced.
// Constraints: a release build ignores the variable entirely; honouring it
//
//	there would let an environment variable downgrade custody.
//
// SPORT: SECRETS_CUSTODY: ADD (internal/secrets QAFileVaultForced).

package secrets

// testFileVaultEnv names the QA switch.
const testFileVaultEnv = "CASCADE_TEST_FILE_VAULT"

// QAFileVaultForced reports whether custody must be the file vault: true
// only in a devkeys build with the switch set to exactly "1".
func QAFileVaultForced(devkeysBuild bool, getenv func(string) string) bool {
	if !devkeysBuild || getenv == nil {
		return false
	}
	return getenv(testFileVaultEnv) == "1"
}
