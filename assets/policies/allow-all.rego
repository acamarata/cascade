# allow-all.rego
#
# Development override policy: allow every action unconditionally.
#
# Usage: cascade policy add path/to/allow-all.rego
#
# WARNING: This policy disables guardrails entirely. Use only in isolated
# development environments where you want to test dispatch without restrictions.
#
# SPORT: MASTER-POLICIES.md

package cascade.policy

import future.keywords

# Allow everything unconditionally.
default allow = {"decision": "ALLOW", "reason": "allow-all override active", "policy_id": "allow-all"}
