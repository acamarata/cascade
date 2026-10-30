package registry

import (
	"regexp"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// AuthType identifies a vault credential or a configured gateway source.
type AuthType string

const (
	// AuthKey resolves a static API key through the vault.
	AuthKey AuthType = "key"
	// AuthOAuth resolves a broker-managed grant through the vault.
	AuthOAuth AuthType = "oauth"
	// AuthGateway resolves an account through its call-time source.
	AuthGateway AuthType = "gateway"
)

// Valid reports whether a is one of the three declared auth types.
func (a AuthType) Valid() bool {
	switch a {
	case AuthKey, AuthOAuth, AuthGateway:
		return true
	}
	return false
}

// gatewayAccountRe matches a valid gateway account name per
// contract:gateway-account (P1-GWY-02): lowercase alphanumerics and
// hyphens, 1-32 chars, must start with alphanumeric.
var gatewayAccountRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidateGatewayAuthRef checks that ref is well-formed for an
// AuthGateway record: exactly "gateway:<account>" where account matches
// gatewayAccountRe. Anything else is refused; no vault lookup fallback.
func ValidateGatewayAuthRef(ref VaultKeyRef) error {
	s := string(ref)
	if !strings.HasPrefix(s, "gateway:") {
		return cascade.New(cascade.KindInvalidInput,
			"registry: gateway auth ref must start with \"gateway:\"")
	}
	acct := strings.TrimPrefix(s, "gateway:")
	if acct == "" {
		return cascade.New(cascade.KindInvalidInput,
			"registry: gateway auth ref missing account after \"gateway:\"")
	}
	if strings.Contains(acct, "/") || strings.Contains(acct, "..") {
		return cascade.New(cascade.KindInvalidInput,
			"registry: gateway account contains path characters")
	}
	if !gatewayAccountRe.MatchString(acct) {
		return cascade.New(cascade.KindInvalidInput,
			"registry: gateway account must match ^[a-z0-9][a-z0-9-]{0,31}$")
	}
	return nil
}
