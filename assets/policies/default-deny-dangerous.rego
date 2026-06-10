# default-deny-dangerous.rego
#
# Default Cascade policy: deny known-dangerous action patterns.
#
# Architecture:
#   - Default rule is Allow (explicit deny-list, not an allow-list).
#   - Any matching deny rule overrides the default.
#   - Evaluated against PolicyAction: {action_type, args, context}.
#
# To compile to WASM (developers only — pre-built artifact is checked in):
#   opa build -t wasm -e data/allow default-deny-dangerous.rego -o bundle.tar.gz
#   tar -xf bundle.tar.gz /policy.wasm
#   mv policy.wasm assets/policies/default-deny-dangerous.wasm
#
# SPORT: MASTER-POLICIES.md

package cascade.policy

import future.keywords.if
import future.keywords.in

# Default: allow the action.
default allow = {"decision": "ALLOW", "reason": "", "policy_id": "default-deny-dangerous"}

# Deny patterns for bash action_type.
allow = {"decision": "DENY", "reason": "recursive delete from root is forbidden", "policy_id": "default-deny-dangerous"} if {
    input.action_type == "bash"
    contains(json.marshal(input.args), "rm -rf /")
}

allow = {"decision": "DENY", "reason": "recursive delete of home directory is forbidden", "policy_id": "default-deny-dangerous"} if {
    input.action_type == "bash"
    contains(json.marshal(input.args), "rm -rf ~")
}

allow = {"decision": "DENY", "reason": "force-push to main branch is forbidden", "policy_id": "default-deny-dangerous"} if {
    input.action_type == "bash"
    contains(json.marshal(input.args), "git push --force origin main")
}

allow = {"decision": "DENY", "reason": "force-push to master branch is forbidden", "policy_id": "default-deny-dangerous"} if {
    input.action_type == "bash"
    contains(json.marshal(input.args), "git push --force origin master")
}

allow = {"decision": "DENY", "reason": "DROP TABLE is forbidden", "policy_id": "default-deny-dangerous"} if {
    contains(json.marshal(input.args), "DROP TABLE")
}

allow = {"decision": "DENY", "reason": "DROP DATABASE is forbidden", "policy_id": "default-deny-dangerous"} if {
    contains(json.marshal(input.args), "DROP DATABASE")
}

allow = {"decision": "DENY", "reason": "TRUNCATE is forbidden", "policy_id": "default-deny-dangerous"} if {
    contains(json.marshal(input.args), "TRUNCATE")
}

allow = {"decision": "DENY", "reason": "redirecting to /dev/null is forbidden", "policy_id": "default-deny-dangerous"} if {
    input.action_type == "bash"
    contains(json.marshal(input.args), "> /dev/null")
}

allow = {"decision": "DENY", "reason": "raw disk overwrite is forbidden", "policy_id": "default-deny-dangerous"} if {
    input.action_type == "bash"
    contains(json.marshal(input.args), "dd of=/dev/disk")
}

allow = {"decision": "DENY", "reason": "filesystem formatting is forbidden", "policy_id": "default-deny-dangerous"} if {
    input.action_type == "bash"
    contains(json.marshal(input.args), "mkfs")
}
