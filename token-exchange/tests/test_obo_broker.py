"""Unit tests for OBOBroker.

These rely on the demo scope policy installed by ``conftest.py``
(``users.read`` -> {readonly, admin}, ``users.write`` -> {admin}).
"""
import time
from unittest.mock import MagicMock

import jwt
import pytest

from broker.cache import TokenCache
from exceptions.errors import (
    VerifyAuthenticationError,
    VerifyAuthorizationError,
    VerifyTokenExchangeError,
)
from verify.obo_broker import OBOBroker
from verify.verify_client import IBMVerifyClient


def _make_jwt(
    exp_offset: int = 3600,
    groups: list[str] | str | None = None,
) -> str:
    payload: dict = {"sub": "user", "exp": int(time.time()) + exp_offset}
    if groups is not None:
        payload["groups"] = groups
    return jwt.encode(payload, "secret", algorithm="HS256")


def _admin_jwt() -> str:
    return _make_jwt(groups=["admin"])


def _readonly_jwt() -> str:
    return _make_jwt(groups=["readonly"])


@pytest.fixture()
def mock_verify_client():
    return MagicMock(spec=IBMVerifyClient)


@pytest.fixture()
def fresh_cache():
    return TokenCache(maxsize=10, ttl=60)


class TestExchangeOBOToken:
    def test_cache_miss_calls_verify_and_caches(self, mock_verify_client, fresh_cache):
        access_token = _make_jwt()
        subject_token = _admin_jwt()
        mock_verify_client.exchange_obo_token.return_value = {"access_token": access_token}

        broker = OBOBroker(verify_client=mock_verify_client, cache=fresh_cache)
        result = broker.exchange_obo_token(subject_token, "actor-tok", "users.read")

        assert result.access_token == access_token
        assert result.cached is False
        mock_verify_client.exchange_obo_token.assert_called_once_with(
            subject_token, "actor-tok", "users.read"
        )

    def test_cache_hit_skips_verify(self, mock_verify_client, fresh_cache):
        access_token = _make_jwt()
        subject_token = _admin_jwt()
        # Pre-populate using the same composed key the broker will compute.
        fresh_cache.set(subject_token, "actor-tok|users.read", access_token)

        broker = OBOBroker(verify_client=mock_verify_client, cache=fresh_cache)
        result = broker.exchange_obo_token(subject_token, "actor-tok", "users.read")

        assert result.access_token == access_token
        assert result.cached is True
        mock_verify_client.exchange_obo_token.assert_not_called()

    def test_verify_auth_error_propagates(self, mock_verify_client, fresh_cache):
        mock_verify_client.exchange_obo_token.side_effect = VerifyAuthenticationError("401")

        broker = OBOBroker(verify_client=mock_verify_client, cache=fresh_cache)
        with pytest.raises(VerifyAuthenticationError):
            broker.exchange_obo_token(_admin_jwt(), "actor-tok", "users.read")

    def test_verify_exchange_error_retries_then_raises(self, mock_verify_client, fresh_cache):
        mock_verify_client.exchange_obo_token.side_effect = VerifyTokenExchangeError("server error")

        broker = OBOBroker(verify_client=mock_verify_client, cache=fresh_cache)
        with pytest.raises(VerifyTokenExchangeError):
            broker.exchange_obo_token(_admin_jwt(), "actor-tok", "users.read")

        # tenacity retries 3 times
        assert mock_verify_client.exchange_obo_token.call_count == 3

    def test_different_token_pairs_use_separate_cache_entries(self, mock_verify_client, fresh_cache):
        token_a = _make_jwt()
        token_b = _make_jwt()
        subject_a = _make_jwt(groups=["admin"])
        subject_b = _make_jwt(groups=["admin"])
        mock_verify_client.exchange_obo_token.side_effect = [
            {"access_token": token_a},
            {"access_token": token_b},
        ]

        broker = OBOBroker(verify_client=mock_verify_client, cache=fresh_cache)
        result_a = broker.exchange_obo_token(subject_a, "actor-a", "users.read")
        result_b = broker.exchange_obo_token(subject_b, "actor-b", "users.read")

        assert result_a.access_token == token_a
        assert result_b.access_token == token_b
        assert mock_verify_client.exchange_obo_token.call_count == 2

    def test_same_subject_actor_different_scopes_use_separate_cache_entries(
        self, mock_verify_client, fresh_cache
    ):
        read_token = _make_jwt()
        write_token = _make_jwt()
        subject_token = _admin_jwt()
        mock_verify_client.exchange_obo_token.side_effect = [
            {"access_token": read_token},
            {"access_token": write_token},
        ]

        broker = OBOBroker(verify_client=mock_verify_client, cache=fresh_cache)
        result_read = broker.exchange_obo_token(subject_token, "actor", "users.read")
        result_write = broker.exchange_obo_token(subject_token, "actor", "users.write")

        assert result_read.access_token == read_token
        assert result_write.access_token == write_token
        assert result_read.cached is False
        assert result_write.cached is False
        assert mock_verify_client.exchange_obo_token.call_count == 2

    def test_scope_normalization_yields_cache_hit_across_orderings(
        self, mock_verify_client, fresh_cache
    ):
        access_token = _make_jwt()
        subject_token = _admin_jwt()
        mock_verify_client.exchange_obo_token.return_value = {"access_token": access_token}

        broker = OBOBroker(verify_client=mock_verify_client, cache=fresh_cache)
        first = broker.exchange_obo_token(subject_token, "actor", "users.write users.read")
        second = broker.exchange_obo_token(subject_token, "actor", "users.read users.write")

        assert first.cached is False
        assert second.cached is True
        # Verify is called only once because the second call hits the cache.
        assert mock_verify_client.exchange_obo_token.call_count == 1
        # Normalized scope is sorted alphabetically.
        mock_verify_client.exchange_obo_token.assert_called_once_with(
            subject_token, "actor", "users.read users.write"
        )


class TestAuthorizationCheck:
    def test_authz_allows_readonly_user_for_read_scope(self, mock_verify_client, fresh_cache):
        access_token = _make_jwt()
        mock_verify_client.exchange_obo_token.return_value = {"access_token": access_token}

        broker = OBOBroker(verify_client=mock_verify_client, cache=fresh_cache)
        result = broker.exchange_obo_token(_readonly_jwt(), "actor-tok", "users.read")

        assert result.access_token == access_token
        mock_verify_client.exchange_obo_token.assert_called_once()

    def test_authz_denies_readonly_user_for_write_scope(self, mock_verify_client, fresh_cache):
        broker = OBOBroker(verify_client=mock_verify_client, cache=fresh_cache)
        with pytest.raises(VerifyAuthorizationError):
            broker.exchange_obo_token(_readonly_jwt(), "actor-tok", "users.write")

        mock_verify_client.exchange_obo_token.assert_not_called()

    def test_authz_allows_admin_when_groups_claim_is_scalar_string(
        self, mock_verify_client, fresh_cache
    ):
        # IBM Verify emits ``groups`` as a scalar (``"admin"``) when the user
        # belongs to a single group; both users.read and users.write must work.
        access_token = _make_jwt()
        subject_token = _make_jwt(groups="admin")
        mock_verify_client.exchange_obo_token.return_value = {"access_token": access_token}

        broker = OBOBroker(verify_client=mock_verify_client, cache=fresh_cache)
        result = broker.exchange_obo_token(
            subject_token, "actor-tok", "users.read users.write"
        )

        assert result.access_token == access_token
        mock_verify_client.exchange_obo_token.assert_called_once()

    def test_authz_denies_when_groups_claim_missing(self, mock_verify_client, fresh_cache):
        # JWT without a groups claim
        subject_token = _make_jwt()

        broker = OBOBroker(verify_client=mock_verify_client, cache=fresh_cache)
        with pytest.raises(VerifyAuthorizationError):
            broker.exchange_obo_token(subject_token, "actor-tok", "users.read")

        mock_verify_client.exchange_obo_token.assert_not_called()

    def test_authz_denies_when_unknown_scope_requested(self, mock_verify_client, fresh_cache):
        broker = OBOBroker(verify_client=mock_verify_client, cache=fresh_cache)
        with pytest.raises(VerifyAuthorizationError):
            broker.exchange_obo_token(_admin_jwt(), "actor-tok", "users.delete")

        mock_verify_client.exchange_obo_token.assert_not_called()

    def test_authz_denies_partial_match_in_multi_scope(self, mock_verify_client, fresh_cache):
        # readonly is allowed for users.read but not users.write; the multi-scope
        # request must fail because every scope must be authorized.
        broker = OBOBroker(verify_client=mock_verify_client, cache=fresh_cache)
        with pytest.raises(VerifyAuthorizationError):
            broker.exchange_obo_token(
                _readonly_jwt(), "actor-tok", "users.read users.write"
            )

        mock_verify_client.exchange_obo_token.assert_not_called()

    def test_authz_check_runs_before_cache_lookup(self, mock_verify_client, fresh_cache):
        # Pre-populate the cache as if a prior (now-revoked) request had succeeded.
        subject_token = _readonly_jwt()
        fresh_cache.set(subject_token, "actor-tok|users.write", _make_jwt())

        broker = OBOBroker(verify_client=mock_verify_client, cache=fresh_cache)
        with pytest.raises(VerifyAuthorizationError):
            broker.exchange_obo_token(subject_token, "actor-tok", "users.write")

        mock_verify_client.exchange_obo_token.assert_not_called()
