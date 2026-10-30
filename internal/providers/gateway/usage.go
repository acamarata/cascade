package gateway

import (
	"net/url"
	"strings"
)

func parseUsage(name string, raw any) (UsageSource, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return UsageSource{}, invalid(name+".usage", "must be a table")
	}
	s, err := stringsOnly(name+".usage", m, "source window path used_pointer limit_pointer utilization_pointer resets_at_pointer utilization_header resets_header")
	if err != nil {
		return UsageSource{}, err
	}
	if s["source"] != "endpoint" && s["source"] != "headers" {
		return UsageSource{}, invalid(name+".usage.source", "unsupported value")
	}
	switch s["window"] {
	case "5h", "day", "week", "month":
	default:
		return UsageSource{}, invalid(name+".usage.window", "unsupported value")
	}
	for k, v := range s {
		endpoint := k == "path" || strings.HasSuffix(k, "_pointer")
		header := strings.HasSuffix(k, "_header")
		if s["source"] == "headers" && endpoint || s["source"] == "endpoint" && header {
			return UsageSource{}, invalid(name+".usage."+k, "not valid for this source")
		}
		if strings.HasSuffix(k, "_pointer") && !validPointer(v) {
			return UsageSource{}, invalid(name+".usage."+k, "invalid RFC 6901 pointer")
		}
		if header && (v == "" || strings.ContainsAny(v, "\r\n: ")) {
			return UsageSource{}, invalid(name+".usage."+k, "invalid header name")
		}
	}
	if s["source"] == "endpoint" {
		p, err := url.Parse(s["path"])
		if err != nil || !strings.HasPrefix(s["path"], "/") || strings.HasPrefix(s["path"], "//") || p.Host != "" || p.Fragment != "" {
			return UsageSource{}, invalid(name+".usage.path", "must be a relative absolute-path reference")
		}
	}
	return UsageSource{Kind: s["source"], Window: s["window"], Path: s["path"], UsedPointer: s["used_pointer"],
		LimitPointer: s["limit_pointer"], UtilizationPointer: s["utilization_pointer"], ResetsAtPointer: s["resets_at_pointer"],
		UtilizationHeader: s["utilization_header"], ResetsHeader: s["resets_header"]}, nil
}

func validPointer(s string) bool {
	if s != "" && s[0] != '/' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] == '~' {
			i++
			if i == len(s) || s[i] != '0' && s[i] != '1' {
				return false
			}
		}
	}
	return true
}
