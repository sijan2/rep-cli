package governor

import (
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Rate is what a response said about the caller's budget. Limit and Remaining
// are -1 when the header family did not supply them, because 0 remaining is a
// real signal that must not be confused with absence.
type Rate struct {
	HeadersPresent bool
	Limit          int
	Remaining      int
	ResetAt        time.Time
	RetryAfter     time.Duration
	Family         string // retry-after|ratelimit|x-ratelimit|x-rate-limit
}

// ParseRate reads, in order of preference, the IETF RateLimit draft (the
// "limit=, remaining=, reset=" item form, the newer "r=;t=" form, the legacy
// RateLimit-Limit/-Remaining/-Reset trio and RateLimit-Policy), Reddit's
// x-ratelimit-* (reset is seconds, possibly fractional), X's x-rate-limit-*
// (reset is a unix epoch), and RFC 9110 Retry-After (delta-seconds or an
// HTTP-date). Family names the budget headers when any were present, otherwise
// retry-after.
func ParseRate(h http.Header, now time.Time) Rate {
	r := Rate{Limit: -1, Remaining: -1}
	families := []func(http.Header, time.Time, *Rate) bool{parseIETF, parseReddit, parseX}
	for _, parse := range families {
		if parse(h, now, &r) {
			r.HeadersPresent = true
			break
		}
	}
	if v := headerGet(h, "Retry-After"); v != "" {
		if d, at, ok := parseRetryAfter(v, now); ok {
			r.HeadersPresent = true
			r.RetryAfter = d
			if r.ResetAt.IsZero() {
				r.ResetAt = at
			}
			if r.Family == "" {
				r.Family = "retry-after"
			}
		}
	}
	return r
}

func parseIETF(h http.Header, now time.Time, r *Rate) bool {
	found := false
	if v := headerGet(h, "RateLimit"); v != "" {
		found = true
		d := parseDict(v)
		setInt(&r.Limit, d, "limit", "l")
		setInt(&r.Remaining, d, "remaining", "r")
		if secs, ok := firstFloat(d, "reset", "t"); ok {
			r.ResetAt = now.Add(seconds(secs))
		}
	}
	if v := headerGet(h, "RateLimit-Policy"); v != "" {
		found = true
		d := parseDict(v)
		if r.Limit < 0 {
			setInt(&r.Limit, d, "limit", "q", "")
		}
	}
	for name, dst := range map[string]*int{"RateLimit-Limit": &r.Limit, "RateLimit-Remaining": &r.Remaining} {
		if v := headerGet(h, name); v != "" {
			found = true
			if n, ok := parseInt(leadingItem(v)); ok {
				*dst = n
			}
		}
	}
	if v := headerGet(h, "RateLimit-Reset"); v != "" {
		found = true
		if secs, ok := parseFloat(leadingItem(v)); ok {
			r.ResetAt = now.Add(seconds(secs))
		}
	}
	if found {
		r.Family = "ratelimit"
	}
	return found
}

func parseReddit(h http.Header, now time.Time, r *Rate) bool {
	return parseTrio(h, "x-ratelimit", r, func(reset float64) time.Time { return now.Add(seconds(reset)) })
}

func parseX(h http.Header, _ time.Time, r *Rate) bool {
	return parseTrio(h, "x-rate-limit", r, func(reset float64) time.Time { return time.Unix(int64(reset), 0) })
}

func parseTrio(h http.Header, prefix string, r *Rate, resetAt func(float64) time.Time) bool {
	found := false
	if v := headerGet(h, prefix+"-limit"); v != "" {
		found = true
		if n, ok := parseInt(v); ok {
			r.Limit = n
		}
	}
	if v := headerGet(h, prefix+"-remaining"); v != "" {
		found = true
		if n, ok := parseInt(v); ok {
			r.Remaining = n
		}
	}
	if v := headerGet(h, prefix+"-reset"); v != "" {
		found = true
		if f, ok := parseFloat(v); ok {
			r.ResetAt = resetAt(f)
		}
	}
	if found {
		r.Family = prefix
	}
	return found
}

func parseRetryAfter(v string, now time.Time) (time.Duration, time.Time, bool) {
	v = strings.TrimSpace(v)
	if secs, ok := parseFloat(v); ok && secs >= 0 {
		d := seconds(secs)
		return d, now.Add(d), true
	}
	at, err := http.ParseTime(v)
	if err != nil {
		return 0, time.Time{}, false
	}
	d := at.Sub(now)
	if d < 0 {
		d = 0
	}
	return d, at, true
}

// headerGet tolerates non-canonical keys so headers rebuilt from a ledger entry
// or a map literal are still seen.
func headerGet(h http.Header, name string) string {
	if v := h.Get(name); v != "" {
		return v
	}
	for k, vs := range h {
		if strings.EqualFold(k, name) && len(vs) > 0 {
			return vs[0]
		}
	}
	return ""
}

// parseDict reads a structured-field-like dictionary ("limit=10, remaining=0,
// reset=7" or `"default";r=0;t=7`) into lowercase keys. A bare leading token
// with no "=" is stored under "" so RateLimit-Policy's quota can be found.
func parseDict(v string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ';' }) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			if _, isNum := parseFloat(part); isNum {
				if _, taken := out[""]; !taken {
					out[""] = part
				}
			}
			continue
		}
		out[strings.ToLower(strings.TrimSpace(key))] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	return out
}

func setInt(dst *int, d map[string]string, keys ...string) {
	for _, k := range keys {
		if v, ok := d[k]; ok {
			if n, ok := parseInt(v); ok {
				*dst = n
				return
			}
		}
	}
}

func firstFloat(d map[string]string, keys ...string) (float64, bool) {
	for _, k := range keys {
		if v, ok := d[k]; ok {
			if f, ok := parseFloat(v); ok {
				return f, true
			}
		}
	}
	return 0, false
}

func leadingItem(v string) string {
	item, _, _ := strings.Cut(v, ";")
	item, _, _ = strings.Cut(item, ",")
	return strings.TrimSpace(item)
}

// parseInt accepts Reddit's fractional counters ("996.0") by truncation.
func parseInt(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if n, err := strconv.Atoi(s); err == nil {
		return n, true
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
		return int(f), true
	}
	return 0, false
}

func parseFloat(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

func seconds(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

// Stop is a reason to end the run now. Until is zero when no cooldown applies.
type Stop struct {
	Code   string // rate_limited|auth|challenge
	Until  time.Time
	Reason string
}

const (
	defaultRateLimitCooldown = 60 * time.Second
	challengeCooldown        = 120 * time.Second
)

// Observe classifies one response and persists any cooldown it implies. It
// never sleeps and never retries; the caller stops. Precedence: a challenge
// (cf-mitigated or the known interstitial phrases) beats a 429, which beats
// 401/403 and redirects to a login page. nil means carry on.
func (g *Governor) Observe(site string, status int, h http.Header, bodyHint string) *Stop {
	now := g.now()
	hint := strings.ToLower(bodyHint)
	switch {
	case headerGet(h, "cf-mitigated") != "":
		return g.stop(site, "challenge", now.Add(challengeCooldown), "cf-mitigated "+headerGet(h, "cf-mitigated"))
	case strings.Contains(hint, "prove your humanity"):
		return g.stop(site, "challenge", now.Add(challengeCooldown), "prove your humanity")
	case strings.Contains(hint, "blocked by network security"):
		return g.stop(site, "challenge", now.Add(challengeCooldown), "blocked by network security")
	case status == 429:
		rate := ParseRate(h, now)
		until, reason := now.Add(defaultRateLimitCooldown), "http 429 default"
		switch {
		case rate.RetryAfter > 0:
			until, reason = now.Add(rate.RetryAfter), "http 429 retry-after"
		case rate.ResetAt.After(now):
			until, reason = rate.ResetAt, "http 429 "+rate.Family+" reset"
		}
		return g.stop(site, "rate_limited", until, reason)
	case status == 401 || status == 403:
		return &Stop{Code: "auth", Reason: fmt.Sprintf("http %d", status)}
	case status == 301 || status == 302 || status == 303 || status == 307 || status == 308:
		if path, ok := loginRedirect(headerGet(h, "Location")); ok {
			return &Stop{Code: "auth", Reason: "redirect to " + path}
		}
	}
	return nil
}

// loginRedirect recognizes the login walls the shipped policies name. Only the
// path is reported so a Location carrying tokens never reaches output.
func loginRedirect(location string) (string, bool) {
	if location == "" {
		return "", false
	}
	u, err := url.Parse(location)
	if err != nil {
		return "", false
	}
	path := strings.ToLower(u.Path)
	if strings.HasPrefix(path, "/login") || strings.HasPrefix(path, "/i/flow/login") || u.Query().Has("rdt") {
		return u.Path, true
	}
	return "", false
}

// stop persists the cooldown under the shared clamp (so Until is what the
// file says). A persistence failure is reported in Reason rather than hidden,
// and never escalates into a retry.
func (g *Governor) stop(site, code string, until time.Time, reason string) *Stop {
	until = g.clampCooldown(site, until)
	if err := g.SetCooldown(site, until, reason); err != nil {
		reason += " (cooldown not persisted: " + err.Error() + ")"
	}
	return &Stop{Code: code, Until: until, Reason: reason}
}
