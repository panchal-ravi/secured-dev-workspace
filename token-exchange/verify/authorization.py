"""Local authorization gate for IBM Verify OBO token exchange.

Compares the ``groups`` claim embedded in a caller's ``subject_token`` against
the scope set being requested, using a policy loaded from a JSON file at
startup. Group comparison is case-insensitive (both sides lowercased), matching
the portal's group handling of IBM Verify's lowercased ``groups`` claim. The
check fails closed: a missing/malformed claim, an unknown scope, an empty
policy, or groups disjoint from a scope's required set all raise
:class:`VerifyAuthorizationError` (mapped to HTTP 403 in the API layer).
"""
import json

import jwt

from exceptions.errors import VerifyAuthorizationError

# scope -> frozenset of allowed group names (lowercased). Populated at startup
# via ``set_scope_policy(load_scope_policy(...))``. Empty until then, so every
# exchange is denied until a policy has been loaded — fail closed by default.
ScopePolicy = dict[str, frozenset[str]]
_scope_policy: ScopePolicy = {}


def load_scope_policy(path: str) -> ScopePolicy:
    """Load and validate the scope->allowed-groups policy from *path*.

    Group names are lowercased so the runtime comparison is case-insensitive.
    Raises (``FileNotFoundError`` / ``json.JSONDecodeError`` / ``ValueError``)
    on a missing or malformed file so the service aborts startup rather than
    booting with no enforceable policy.
    """
    with open(path, encoding="utf-8") as fh:
        raw = json.load(fh)
    if not isinstance(raw, dict):
        raise ValueError(f"scope policy {path!r} must be a JSON object")
    policy: ScopePolicy = {}
    for scope, groups in raw.items():
        if (
            not isinstance(scope, str)
            or not isinstance(groups, list)
            or not all(isinstance(g, str) for g in groups)
        ):
            raise ValueError(
                f"scope policy entry {scope!r} must map a scope string to a list "
                "of group-name strings"
            )
        policy[scope] = frozenset(g.lower() for g in groups)
    return policy


def set_scope_policy(policy: ScopePolicy) -> None:
    """Install *policy* as the active scope->groups map (called at startup)."""
    global _scope_policy
    _scope_policy = policy


def _groups_from_token(token: str) -> list[str] | None:
    """Return the ``groups`` claim from *token* without signature verification.

    Accepts both a JSON array (``["admin", "readonly"]``) and a scalar string
    (``"admin"`` or ``"admin,readonly"``) — IBM Verify emits the latter when the
    user belongs to a single group.
    """
    try:
        payload = jwt.decode(token, options={"verify_signature": False})
    except Exception:
        return None
    if not isinstance(payload, dict):
        return None
    groups = payload.get("groups")
    if isinstance(groups, str):
        parsed = [g.strip() for g in groups.replace(",", " ").split() if g.strip()]
        return parsed or None
    if isinstance(groups, list) and all(isinstance(g, str) for g in groups):
        return groups
    return None


def authorize_scope(subject_token: str, scope: str) -> None:
    """Raise :class:`VerifyAuthorizationError` if *subject_token*'s groups don't entitle *scope*.

    *scope* is the space-separated string from the request. Every scope token
    must be present in the active policy and at least one of the user's groups
    (case-insensitive) must satisfy each scope's required-groups set.
    """
    groups = _groups_from_token(subject_token)
    if not groups:
        raise VerifyAuthorizationError(
            "subject_token missing or malformed 'groups' claim"
        )

    user_groups = {g.lower() for g in groups}
    requested = [s for s in scope.split() if s]
    for requested_scope in requested:
        required = _scope_policy.get(requested_scope)
        if required is None:
            raise VerifyAuthorizationError(
                f"scope '{requested_scope}' is not permitted by policy"
            )
        if user_groups.isdisjoint(required):
            raise VerifyAuthorizationError(
                f"user groups {sorted(user_groups)} are not authorized for scope "
                f"'{requested_scope}' (requires one of {sorted(required)})"
            )
