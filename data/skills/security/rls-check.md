# /rls-check — Row-Level Security Check

Checks that every database table has Row-Level Security (RLS) policies defined.

## Important: no built-in Supabase integration

Cascade has no native Supabase integration. This skill is prompt-driven:

- **If a Supabase MCP is connected** in your session, use it: list tables via the MCP, then check each table for RLS policies. Flag any table that has zero policies.
- **If no Supabase MCP is connected**, follow the manual steps below.

## With Supabase MCP connected

1. List all tables in the public schema.
2. For each table, fetch its RLS policies.
3. Report any table with `rls_enabled = false` or zero policies as a finding.

Expected output:

| Table | RLS enabled | Policy count | Status |
|---|---|---|---|
| users | yes | 3 | OK |
| messages | yes | 0 | WARN: no policies |
| admin_logs | no | 0 | FAIL: RLS disabled |

## Manual check (no MCP)

1. Open the Supabase dashboard for your project.
2. Go to Database > Tables.
3. For each table, verify the RLS toggle is on and at least one policy exists.
4. Check the Authentication > Policies view for a full policy list.

Tables to check at minimum: any table storing user data, any table referenced by your API, any table with write access.

## Common RLS mistakes

- Table created with RLS disabled (default in Supabase).
- Policy exists but does not cover INSERT or DELETE — only SELECT.
- Service role key used in client-side code, bypassing RLS entirely.
- Policies that reference `auth.uid()` but no auth session is enforced upstream.
