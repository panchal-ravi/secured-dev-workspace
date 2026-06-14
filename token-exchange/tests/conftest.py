"""Shared pytest configuration for token-exchange.

Sets the required settings env vars BEFORE any test module imports
``config.settings`` — constructing ``Settings`` needs a non-empty
``obo_client_secret`` — and installs the demo scope policy used by the broker
and route suites. Authorization-specific tests override the policy in their own
fixtures/bodies.
"""
import os

os.environ.setdefault("TOKEN_EXCHANGE_OBO_CLIENT_SECRET", "test-secret")
os.environ.setdefault("TOKEN_EXCHANGE_VERIFY_BASE_URL", "https://tenant.verify.ibm.com")
os.environ.setdefault("TOKEN_EXCHANGE_OBO_CLIENT_ID", "test-client-id")

import pytest

from verify import authorization

# Mirrors the PoC's former hardcoded SCOPE_REQUIREMENTS so the ported broker
# suite keeps the same users.read / users.write semantics now that the policy
# is file-loaded rather than baked into the module.
_DEMO_POLICY = {
    "users.read": frozenset({"readonly", "admin"}),
    "users.write": frozenset({"admin"}),
}


@pytest.fixture(autouse=True)
def _demo_scope_policy():
    authorization.set_scope_policy(dict(_DEMO_POLICY))
    yield
    authorization.set_scope_policy({})
