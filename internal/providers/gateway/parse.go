package gateway

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

var accountNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
var digestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
var envRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func invalid(key, reason string) error {
	return cascade.Newf(cascade.KindInvalidInput, "gateways.%s: %s", key, reason)
}

func stringsOnly(name string, m map[string]any, allowed string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range m {
		if !strings.Contains(" "+allowed+" ", " "+k+" ") {
			return nil, invalid(name+"."+k, "unknown key")
		}
		if k == "usage" || k == "models" {
			continue
		}
		s, ok := v.(string)
		if !ok {
			return nil, invalid(name+"."+k, "must be a string")
		}
		out[k] = s
	}
	return out, nil
}

func parseOneAccount(name string, m map[string]any) (Account, error) {
	s, err := stringsOnly(name, m, "base_url header key_env credential_helper helper_sha256 models usage")
	if err != nil {
		return Account{}, err
	}
	id, err := normalizeBaseURL(s["base_url"])
	if err != nil {
		return Account{}, invalid(name+".base_url", err.Error())
	}
	a := Account{Name: name, ID: id, BaseURL: s["base_url"], Header: HeaderBearer, TermsSensitive: true}
	if h, present := s["header"]; present {
		if h != string(HeaderBearer) && h != string(HeaderXAPIKey) {
			return Account{}, invalid(name+".header", "unsupported value")
		}
		a.Header = HeaderStyle(h)
	}
	a.Credential, err = parseCredential(name, s)
	if err != nil {
		return Account{}, err
	}
	if raw, ok := m["models"]; ok {
		a.Models, err = parseModels(name, raw)
		if err != nil {
			return Account{}, err
		}
	}
	if raw, ok := m["usage"]; ok {
		a.Usage, err = parseUsage(name, raw)
		if err != nil {
			return Account{}, err
		}
	}
	return a, nil
}

func parseCredential(name string, s map[string]string) (Credential, error) {
	env, hasEnv := s["key_env"]
	helper, hasHelper := s["credential_helper"]
	if hasEnv == hasHelper {
		return Credential{}, invalid(name, "exactly one of key_env or credential_helper is required")
	}
	if hasEnv {
		if !envRE.MatchString(env) {
			return Credential{}, invalid(name+".key_env", "invalid environment variable name")
		}
		if _, ok := s["helper_sha256"]; ok {
			return Credential{}, invalid(name+".helper_sha256", "requires credential_helper")
		}
		return Credential{Kind: CredentialEnv, EnvVar: env}, nil
	}
	if !filepath.IsAbs(helper) {
		return Credential{}, invalid(name+".credential_helper", "must be an absolute path")
	}
	if !digestRE.MatchString(s["helper_sha256"]) {
		return Credential{}, invalid(name+".helper_sha256", "requires 64 lowercase hexadecimal digits")
	}
	return Credential{Kind: CredentialHelper, HelperPath: helper, HelperSHA256: s["helper_sha256"]}, nil
}

func parseModels(name string, raw any) ([]string, error) {
	var models []string
	switch list := raw.(type) {
	case []string:
		models = append([]string{}, list...)
	case []any:
		models = []string{}
		for _, v := range list {
			s, ok := v.(string)
			if !ok {
				return nil, invalid(name+".models", "must contain strings")
			}
			models = append(models, s)
		}
	default:
		return nil, invalid(name+".models", "must be a list")
	}
	for _, s := range models {
		if s == "" {
			return nil, invalid(name+".models", "must contain non-empty strings")
		}
	}
	return models, nil
}

func normalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid URL")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("scheme must be https or http")
	}
	if u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", fmt.Errorf("requires a host and no userinfo, query or fragment")
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme == "http" && host != "127.0.0.1" && host != "::1" && host != "localhost" {
		return "", fmt.Errorf("http is allowed only for loopback literals")
	}
	port := u.Port()
	if u.Scheme == "https" && port == "443" || u.Scheme == "http" && port == "80" {
		port = ""
	}
	u.Host = host
	if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	}
	if port != "" {
		u.Host += ":" + port
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	return u.String(), nil
}
