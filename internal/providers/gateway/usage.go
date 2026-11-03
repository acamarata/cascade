package gateway

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/providers/usage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// UsageSourceEndpoint is the usage source kind this build implements: a
// GET of the account's usage path, decoded over RFC 6901 pointers.
const UsageSourceEndpoint = "endpoint"

// UsageSourceHeaders is the per-response header kind. No capture of it
// exists, so it parses (the config family accepts it) but every use of it
// is refused with KindUnsupported.
const UsageSourceHeaders = "headers"

// ErrUsageSourceUnsupported refuses the headers source kind.
var ErrUsageSourceUnsupported = cascade.New(cascade.KindUnsupported,
	"gateway usage: this build reads no headers source kind (no capture of it exists); use source = \"endpoint\"")

// CheckUsageSource refuses a source kind this build cannot read. An
// account with no usage source passes.
func CheckUsageSource(src UsageSource) error {
	if src.Kind == UsageSourceHeaders {
		_, err := DecodeHeaders(src, nil, time.Time{})
		return err
	}
	return nil
}

// DecodeHeaders is the headers kind's decoder; it always refuses with
// ErrUsageSourceUnsupported and keeps no header value.
func DecodeHeaders(src UsageSource, h map[string]string, now time.Time) (usage.QuotaObservation, error) {
	_, _, _ = src, h, now
	return usage.QuotaObservation{}, ErrUsageSourceUnsupported
}

// DecodeEndpoint decodes an endpoint usage body into one observation of
// src.Window, observed at now. An absent pointer, or one naming a missing
// field, leaves that figure unknown; a body that is not JSON, or a field of
// the wrong type, is KindIntegrity with no body content in the message.
func DecodeEndpoint(src UsageSource, body []byte, now time.Time) (usage.QuotaObservation, error) {
	if src.Kind != UsageSourceEndpoint {
		return usage.QuotaObservation{}, ErrUsageSourceUnsupported
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return usage.QuotaObservation{}, cascade.New(cascade.KindIntegrity, "gateway usage: body is not JSON")
	}
	obs := usage.QuotaObservation{Window: topology.BucketWindow(src.Window), ObservedAt: now,
		Used: usage.QuotaUnknown, Limit: usage.QuotaUnknown, UtilizationPct: usage.QuotaUnknown}
	var err error
	if obs.Used, err = pointerInt(doc, src.UsedPointer, "used"); err != nil {
		return usage.QuotaObservation{}, err
	}
	if obs.Limit, err = pointerInt(doc, src.LimitPointer, "limit"); err != nil {
		return usage.QuotaObservation{}, err
	}
	if obs.UtilizationPct, err = pointerFloat(doc, src.UtilizationPointer, "utilization"); err != nil {
		return usage.QuotaObservation{}, err
	}
	if obs.ResetsAt, err = pointerTime(doc, src.ResetsAtPointer); err != nil {
		return usage.QuotaObservation{}, err
	}
	obs.UtilizationPct = obs.Utilization()
	return obs, nil
}

func resolvePointer(doc any, ptr string) (any, bool) {
	if ptr == "" {
		return nil, false
	}
	cur := doc
	for _, tok := range strings.Split(ptr[1:], "/") {
		tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[tok]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(v) {
				return nil, false
			}
			cur = v[i]
		default:
			return nil, false
		}
	}
	return cur, cur != nil
}

func pointerFloat(doc any, ptr, field string) (float64, error) {
	v, ok := resolvePointer(doc, ptr)
	if !ok {
		return usage.QuotaUnknown, nil
	}
	n, isNum := v.(json.Number)
	f, err := n.Float64()
	if !isNum || err != nil || f < 0 {
		return 0, cascade.Newf(cascade.KindIntegrity, "gateway usage: %s is not a non-negative number", field)
	}
	return f, nil
}

func pointerInt(doc any, ptr, field string) (int64, error) {
	f, err := pointerFloat(doc, ptr, field)
	return int64(f), err
}

func pointerTime(doc any, ptr string) (time.Time, error) {
	v, ok := resolvePointer(doc, ptr)
	if !ok {
		return time.Time{}, nil
	}
	switch t := v.(type) {
	case string:
		at, err := time.Parse(time.RFC3339, t)
		if err == nil {
			return at.UTC(), nil
		}
	case json.Number:
		if sec, err := t.Int64(); err == nil && sec > 0 {
			return time.Unix(sec, 0).UTC(), nil
		}
	}
	return time.Time{}, cascade.New(cascade.KindIntegrity, "gateway usage: resets_at is not an RFC 3339 time or unix seconds")
}

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
