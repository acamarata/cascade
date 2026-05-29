# claude-usage: Add OAuth Token Refresh

**Source:** AI / Investigation (2026-04-15)
**Project:** nself (global tooling)
**Status:** raw

## Problem

All 4 Claude Max account OAuth access tokens are expired (acct1: 41h ago, acct2-4: ~293h ago). The `~/bin/claude-usage` script queries `/api/oauth/usage` but never refreshes expired tokens. The API returns auth errors, which the script silently swallows as null usage data. The `claude-usage-refresh` script then crashes on NoneType.

## Fix Required

1. Before querying usage, check `expiresAt` (epoch ms) against current time
2. If expired, POST to `https://console.anthropic.com/api/oauth/token` with:
   - `grant_type: refresh_token`
   - `refresh_token: <from keychain>`
   - `client_id: 9d1c250a-e61b-44d9-88ed-5944d1962f5e`
3. Write new `accessToken`, `refreshToken`, and `expiresAt` back to Keychain
4. **Critical:** refresh tokens rotate on each use — must write new refresh token back immediately
5. Access tokens valid for 8 hours (28800s)
6. Also fix null-handling in `claude-usage-refresh` summary script so it doesn't crash

## Acc4 Expiry

Account 4 (info@ussunnah.org) subscription expires 2026-04-16 and will NOT renew. Update account pool from 4→3 accounts after expiry. Update:
- `~/.claude/references/account-pool.md`
- `~/bin/claude-usage` (ACCOUNTS/EMAILS arrays)
- `~/bin/claude-usage-refresh`
- Any FORGE/CRUNCH scripts that reference 4 accounts
