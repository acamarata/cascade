// Purpose: cross-platform tests for RunCredentialHelper. Windows-only
// refusal lives here; Unix behaviour is exercised in credhelper_unix_test.go
// under the !windows build tag. Tests never touch the operator keychain,
// secret service or HOME; every helper is a fake script under t.TempDir().
// SPORT: internal/secrets credhelper (P1-GWY-02).
package secrets

import (
	"context"
	"runtime"
	"testing"
)

func TestHelperRefusedOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	spec := HelperSpec{
		Path:    `C:\Program Files\helper.exe`,
		SHA256:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Home:    home,
		User:    "testuser",
		Timeout: 0,
	}
	_, err := RunCredentialHelper(context.Background(), spec)
	if err == nil {
		t.Fatal("expected error on windows")
	}
	if err != ErrHelperRefusedOnWindows || err.Error() != "unsupported: credential helpers are not supported on windows; use key_env" {
		t.Fatalf("expected Windows refusal sentinel, got %v", err)
	}
}
