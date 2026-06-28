# /prelaunch — Pre-Launch Security Checklist

Runs `cascade security prelaunch` and checks the items below before any public launch or production deploy.

## Automated checks (via `cascade security prelaunch`)

| Check | Pass condition |
|---|---|
| Secret scan | Zero hits in working tree + git history HEAD~10 |
| Dep audit | Zero high/critical CVEs |
| Error leak | No stack traces or internal paths in API error responses |
| Env vars | All required vars documented; none hardcoded in source |

## Manual checklist

Run through each item and mark pass/warn/fail:

**Privacy & legal**
- [ ] Privacy policy exists and is reachable from the app
- [ ] Cookie consent present if any tracking/analytics

**Auth & access**
- [ ] Authentication required on all non-public routes
- [ ] Rate limiting on auth endpoints (login, password reset, OTP)
- [ ] CAPTCHA or equivalent on public-facing forms

**Network**
- [ ] CORS restricted to known origins (no wildcard `*` in production)
- [ ] Security headers set: `Content-Security-Policy`, `X-Frame-Options`, `X-Content-Type-Options`, `Strict-Transport-Security`
- [ ] HTTPS enforced; no mixed content

**Data**
- [ ] Row-Level Security enabled on all database tables (see `/rls-check`)
- [ ] User-supplied input validated and sanitised before persistence
- [ ] No PII logged in plaintext

## Output

List each item as PASS, WARN, or FAIL. Any FAIL blocks launch. WARNs require a documented decision.
