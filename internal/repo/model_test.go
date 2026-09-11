package repo

import "testing"

func TestLanguageValid(t *testing.T) {
	valid := []Language{LanguageGo, LanguageJSTS, LanguageRust, LanguagePython, LanguageSwift, LanguageGeneric, LanguageUnknown}
	for _, l := range valid {
		if !l.Valid() {
			t.Errorf("Language(%q).Valid() = false, want true", l)
		}
	}
	if Language("cobol").Valid() {
		t.Error(`Language("cobol").Valid() = true, want false (closed set)`)
	}
	if Language("").Valid() {
		t.Error(`Language("").Valid() = true, want false`)
	}
}
