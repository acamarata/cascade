package runtime

import (
	"errors"
	"testing"
)

func fakeProfileEnv(values map[string]string) profileEnv {
	return func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok && v != ""
	}
}

func TestResolveProfile_Precedence(t *testing.T) {
	tests := []struct {
		name    string
		flag    string
		env     map[string]string
		file    string
		want    Profile
		wantSrc ConfigSource
	}{
		{"flag wins over everything", "server", map[string]string{"CASCADE_PROFILE": "worker"}, "local", ProfileServer, SourceFlag},
		{"env wins over file", "", map[string]string{"CASCADE_PROFILE": "worker"}, "local", ProfileWorker, SourceEnv},
		{"file wins over default", "", nil, "server", ProfileServer, SourceFile},
		{"default when nothing set", "", nil, "", DefaultProfile, SourceDefault},
		{"empty env value falls through to file", "", map[string]string{"CASCADE_PROFILE": ""}, "worker", ProfileWorker, SourceFile},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, src, err := ResolveProfile(tc.flag, fakeProfileEnv(tc.env), tc.file)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("profile = %q, want %q", got, tc.want)
			}
			if src != tc.wantSrc {
				t.Errorf("source = %q, want %q", src, tc.wantSrc)
			}
		})
	}
}

func TestResolveProfile_InvalidHardErrors(t *testing.T) {
	tests := []struct {
		name    string
		flag    string
		env     map[string]string
		file    string
		wantSrc ConfigSource
	}{
		{"invalid flag", "bogus", nil, "", SourceFlag},
		{"invalid env", "", map[string]string{"CASCADE_PROFILE": "bogus"}, "", SourceEnv},
		{"invalid file value", "", nil, "bogus", SourceFile},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ResolveProfile(tc.flag, fakeProfileEnv(tc.env), tc.file)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			var invalid *InvalidProfileError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %v, want *InvalidProfileError", err)
			}
			if invalid.Source != tc.wantSrc {
				t.Errorf("error source = %q, want %q", invalid.Source, tc.wantSrc)
			}
		})
	}
}

func TestProfile_Valid(t *testing.T) {
	valid := []Profile{ProfileLocal, ProfileServer, ProfileWorker}
	for _, p := range valid {
		if !p.Valid() {
			t.Errorf("%q.Valid() = false, want true", p)
		}
	}
	invalid := []Profile{"", "Local", "prod", "server "}
	for _, p := range invalid {
		if p.Valid() {
			t.Errorf("%q.Valid() = true, want false", p)
		}
	}
}

func TestProfile_String(t *testing.T) {
	if ProfileServer.String() != "server" {
		t.Errorf("String() = %q, want %q", ProfileServer.String(), "server")
	}
}
