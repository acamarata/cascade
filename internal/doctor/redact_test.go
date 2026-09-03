package doctor

import (
	"strings"
	"testing"
)

// redactShapeCase is one §D-31 secret-shape example shared by
// TestDoctorRedactEntropyAndSecretShapes and its
// _KVDSNJWTLog continuation (split per Art.10.3's 50-line cap).
type redactShapeCase struct {
	name   string
	input  string
	want   string // substring that must appear in the output
	absent string // substring that must NOT survive
}

// redactShapeCasesPart1 covers entropy, bearer-prefix, and PEM shapes.
var redactShapeCasesPart1 = []redactShapeCase{
	{
		name:   "high entropy token",
		input:  "cache-key: aZ8xQ2mK9pL4vN7rT1wB6yH3sD5fG0jC",
		want:   RedactedEntropy,
		absent: "aZ8xQ2mK9pL4vN7rT1wB6yH3sD5fG0jC",
	},
	{
		name:   "bearer-prefixed key",
		input:  "using token sk-ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef0123456789 for the request",
		want:   RedactedSecret,
		absent: "sk-ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef0123456789",
	},
	{
		name:   "PEM block",
		input:  "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKc=\n-----END PRIVATE KEY-----",
		want:   RedactedSecret,
		absent: "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKc=",
	},
}

// redactShapeCasesPart2 covers key/value, DSN, JWT, log-line, and
// path-embedded shapes.
var redactShapeCasesPart2 = []redactShapeCase{
	{
		name:   "secret-named key value",
		input:  "password=Tr0ub4dor&3ZQXKPLM",
		want:   RedactedSecret,
		absent: "Tr0ub4dor&3ZQXKPLM",
	},
	{
		name:   "DSN with embedded credentials",
		input:  "connecting to postgres://dbuser:S3cr3tP4ssw0rdXyz@db.example.com:5432/appdb now",
		want:   RedactedSecret,
		absent: "S3cr3tP4ssw0rdXyz",
	},
	{
		name:   "JWT",
		input:  "authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dQw4w9WgXcQ_rerferf12345",
		want:   RedactedSecret,
		absent: "eyJzdWIiOiIxMjM0NTY3ODkwIn0",
	},
	{
		name:   "secret inside a log line / error message",
		input:  `ERROR: dial failed for postgres://svc:hunter2superSecretPass@10.0.0.5/prod: connection refused`,
		want:   RedactedSecret,
		absent: "hunter2superSecretPass",
	},
	{
		name:   "secret embedded in a file path",
		input:  "/Users/dev/.secrets/sk-liveTESTTOKEN1234567890abcdefgh/config.toml",
		want:   RedactedSecret,
		absent: "sk-liveTESTTOKEN1234567890abcdefgh",
	},
}

func runRedactShapeCases(t *testing.T, cases []redactShapeCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactText(tc.input)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("RedactText(%q) = %q, want it to contain %q", tc.input, got, tc.want)
			}
			if strings.Contains(got, tc.absent) {
				t.Fatalf("RedactText(%q) = %q, still contains the secret %q", tc.input, got, tc.absent)
			}
		})
	}
}

// TestDoctorRedactEntropyAndSecretShapes covers §D-31's full heuristic
// set, one sub-test per shape named in the ticket contract: a
// high-entropy token, a bearer-prefixed key, and a PEM block.
func TestDoctorRedactEntropyAndSecretShapes(t *testing.T) {
	runRedactShapeCases(t, redactShapeCasesPart1)
}

// TestDoctorRedactEntropyAndSecretShapes_KVDSNJWTLog covers the
// remaining §D-31 shapes: secret-named key/value, DSN, JWT, a secret
// inside a log line, and a secret embedded in a file path. Split from
// TestDoctorRedactEntropyAndSecretShapes to stay under Art.10.3's
// 50-line function cap (R-14.117); both share runRedactShapeCases.
func TestDoctorRedactEntropyAndSecretShapes_KVDSNJWTLog(t *testing.T) {
	runRedactShapeCases(t, redactShapeCasesPart2)
}

func TestRedactValue_SecretNamedKeyRedactsRegardlessOfShape(t *testing.T) {
	// §D-31: "the value side of any key matching password|... is
	// redacted regardless of value shape" — even a short, low-entropy,
	// non-pattern-matching value.
	got := RedactValue("db_password", "abc")
	if got != RedactedSecret {
		t.Fatalf("got %q, want %q for a secret-named key with a low-entropy value", got, RedactedSecret)
	}
}

func TestRedactValue_OrdinaryValuePassesThrough(t *testing.T) {
	got := RedactValue("runtime.profile", "default")
	if got != "default" {
		t.Fatalf("got %q, want the value unchanged", got)
	}
}

func TestRedactValue_EmptyValueUnchanged(t *testing.T) {
	if got := RedactValue("password", ""); got != "" {
		t.Fatalf("got %q, want empty value to pass through unchanged (nothing to leak)", got)
	}
}

func TestFilterAllowedFields_OnlyAllowlistedKeysSurvive(t *testing.T) {
	fields := map[string]string{
		"os":                  "darwin",
		"AWS_SECRET_ACCESS_KEY": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	}
	out := FilterAllowedFields(fields, DefaultAllowedFields())
	if _, ok := out["os"]; !ok {
		t.Fatalf("allowlisted key 'os' missing from output: %+v", out)
	}
	if _, ok := out["AWS_SECRET_ACCESS_KEY"]; ok {
		t.Fatalf("non-allowlisted key must be ABSENT from output entirely, got: %+v", out)
	}
}

func TestFilterAllowedFields_EmptyAllowlistRedactsEverything(t *testing.T) {
	fields := map[string]string{"os": "darwin", "arch": "arm64"}
	out := FilterAllowedFields(fields, nil)
	if len(out) != 0 {
		t.Fatalf("got %+v, want every field dropped when AllowedFields is empty (fail closed)", out)
	}
}

func TestFilterAllowedFields_AllowlistedKeyStillGetsValueRedaction(t *testing.T) {
	allowed := NewAllowedFields("runtime.data_dir")
	out := FilterAllowedFields(map[string]string{
		"runtime.data_dir": "sk-ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef0123456789",
	}, allowed)
	if out["runtime.data_dir"] != RedactedSecret {
		t.Fatalf("got %q, want an allowlisted key's secret-shaped VALUE still redacted (defense in depth)", out["runtime.data_dir"])
	}
}

func TestRedactJoinedLines_PEMBlockSpanningMultipleLogLines(t *testing.T) {
	lines := []string{
		"daemon starting",
		"-----BEGIN PRIVATE KEY-----",
		"MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKc=",
		"-----END PRIVATE KEY-----",
		"daemon ready",
	}
	out := redactJoinedLines(lines)
	joined := strings.Join(out, "\n")
	if strings.Contains(joined, "MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKc=") {
		t.Fatalf("PEM body survived a multi-line log scrub: %q", joined)
	}
	if !strings.Contains(joined, "daemon starting") || !strings.Contains(joined, "daemon ready") {
		t.Fatalf("non-secret lines must survive unchanged: %q", joined)
	}
}

func TestShannonEntropy_KnownValues(t *testing.T) {
	if shannonEntropy("") != 0 {
		t.Fatalf("empty string must have zero entropy")
	}
	if shannonEntropy("aaaaaaaaaaaaaaaaaaaa") >= entropyThreshold {
		t.Fatalf("a repeated character must have near-zero entropy")
	}
}

func TestRedactLines(t *testing.T) {
	out := RedactLines([]string{"password=Tr0ub4dor&3ZQXKPLM", "fine line"})
	if strings.Contains(out[0], "Tr0ub4dor") {
		t.Fatalf("RedactLines did not redact line 0: %q", out[0])
	}
	if out[1] != "fine line" {
		t.Fatalf("RedactLines mutated a benign line: %q", out[1])
	}
}
