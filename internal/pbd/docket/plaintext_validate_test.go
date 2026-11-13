package docket

import (
	"errors"
	"testing"
)

func TestValidatePlainTextAccepts(t *testing.T) {
	if err := ValidatePlainText("title", "Add open filter"); err != nil {
		t.Errorf("ValidatePlainText(title, Add open filter) = %v, want nil", err)
	}
	if err := ValidatePlainText("objective", ""); err != nil {
		t.Errorf("ValidatePlainText(objective, \"\") = %v, want nil", err)
	}
	if err := ValidatePlainText("acceptance", "Use `code` inline"); err != nil {
		t.Errorf("ValidatePlainText(acceptance, Use `code` inline) = %v, want nil", err)
	}
	if err := ValidatePlainText("verify", "a < b"); err != nil {
		t.Errorf("ValidatePlainText(verify, a < b) = %v, want nil", err)
	}
	if err := ValidatePlainText("unknown", "text"); !errors.Is(err, ErrPlainText) {
		t.Errorf("ValidatePlainText(unknown, text) = %v, want ErrPlainText", err)
	}
	if err := ValidatePlainText("", "text"); !errors.Is(err, ErrPlainText) {
		t.Errorf("ValidatePlainText(\"\", text) = %v, want ErrPlainText", err)
	}
	if err := ValidatePlainText("title", "\xff\xfe"); !errors.Is(err, ErrPlainText) {
		t.Errorf("ValidatePlainText(title, invalid UTF-8) = %v, want ErrPlainText", err)
	}
}

func TestValidatePlainTextRejectsHTML(t *testing.T) {
	err := ValidatePlainText("objective", "<b>bold</b>")
	if !errors.Is(err, ErrPlainText) {
		t.Errorf("ValidatePlainText(objective, <b>bold</b>) = %v, want ErrPlainText", err)
	}
	if err.Error() != "ERR_PLAIN_TEXT" {
		t.Errorf("Error() = %q, want ERR_PLAIN_TEXT", err.Error())
	}

	err = ValidatePlainText("title", "<!--comment-->")
	if !errors.Is(err, ErrPlainText) {
		t.Errorf("ValidatePlainText(title, <!--comment-->) = %v, want ErrPlainText", err)
	}
	if err.Error() != "ERR_PLAIN_TEXT" {
		t.Errorf("Error() = %q, want ERR_PLAIN_TEXT", err.Error())
	}
}
