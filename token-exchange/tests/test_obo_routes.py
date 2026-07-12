"""Integration-style tests for the OBO FastAPI route."""
import time
from unittest.mock import patch

import jwt
import pytest
import requests
from httpx import ASGITransport, AsyncClient

from api.main import app
from exceptions.errors import (
    CacheError,
    VerifyAuthenticationError,
    VerifyAuthorizationError,
    VerifyTokenExchangeError,
)
from verify.obo_broker import OBOTokenResult


def _make_jwt(exp_offset: int = 3600) -> str:
    payload = {"sub": "user", "exp": int(time.time()) + exp_offset}
    return jwt.encode(payload, "secret", algorithm="HS256")


@pytest.fixture()
def async_client():
    transport = ASGITransport(app=app)
    return AsyncClient(transport=transport, base_url="http://test")


_OBO_PAYLOAD = {
    "subject_token": "eyJ.subject.token",
    "actor_token": "eyJ.actor.token",
    "scope": "users.read",
}


class TestExchangeOBOToken:
    async def test_success_cache_miss(self, async_client):
        access_token = _make_jwt()
        result = OBOTokenResult(access_token=access_token, cached=False)

        with patch("api.routes._obo_broker.exchange_obo_token", return_value=result):
            async with async_client as client:
                resp = await client.post("/v1/identity/obo-token", json=_OBO_PAYLOAD)

        assert resp.status_code == 200
        body = resp.json()
        assert body["access_token"] == access_token
        assert body["cached"] is False

    async def test_success_cache_hit(self, async_client):
        access_token = _make_jwt()
        result = OBOTokenResult(access_token=access_token, cached=True)

        with patch("api.routes._obo_broker.exchange_obo_token", return_value=result):
            async with async_client as client:
                resp = await client.post("/v1/identity/obo-token", json=_OBO_PAYLOAD)

        assert resp.status_code == 200
        assert resp.json()["cached"] is True

    async def test_authz_failure_returns_403(self, async_client):
        with patch(
            "api.routes._obo_broker.exchange_obo_token",
            side_effect=VerifyAuthorizationError(
                "user groups ['readonly'] are not authorized for scope 'users.write'"
            ),
        ):
            async with async_client as client:
                resp = await client.post("/v1/identity/obo-token", json=_OBO_PAYLOAD)

        assert resp.status_code == 403
        assert "not authorized" in resp.json()["detail"]

    async def test_auth_failure_returns_401(self, async_client):
        with patch(
            "api.routes._obo_broker.exchange_obo_token",
            side_effect=VerifyAuthenticationError("unauthorized"),
        ):
            async with async_client as client:
                resp = await client.post("/v1/identity/obo-token", json=_OBO_PAYLOAD)

        assert resp.status_code == 401

    async def test_exchange_error_returns_500(self, async_client):
        with patch(
            "api.routes._obo_broker.exchange_obo_token",
            side_effect=VerifyTokenExchangeError("exchange failed"),
        ):
            async with async_client as client:
                resp = await client.post("/v1/identity/obo-token", json=_OBO_PAYLOAD)

        assert resp.status_code == 500

    async def test_cache_error_returns_500(self, async_client):
        with patch(
            "api.routes._obo_broker.exchange_obo_token",
            side_effect=CacheError("cache broke"),
        ):
            async with async_client as client:
                resp = await client.post("/v1/identity/obo-token", json=_OBO_PAYLOAD)

        assert resp.status_code == 500

    async def test_verify_unavailable_returns_503(self, async_client):
        with patch(
            "api.routes._obo_broker.exchange_obo_token",
            side_effect=requests.exceptions.ConnectionError("connection refused"),
        ):
            async with async_client as client:
                resp = await client.post("/v1/identity/obo-token", json=_OBO_PAYLOAD)

        assert resp.status_code == 503

    async def test_request_id_header_is_echoed(self, async_client):
        result = OBOTokenResult(access_token=_make_jwt(), cached=False)
        with patch("api.routes._obo_broker.exchange_obo_token", return_value=result):
            async with async_client as client:
                resp = await client.post(
                    "/v1/identity/obo-token",
                    json=_OBO_PAYLOAD,
                    headers={"X-Request-ID": "req-obo-1"},
                )

        assert resp.status_code == 200
        assert resp.headers["X-Request-ID"] == "req-obo-1"

    async def test_empty_policy_denies_with_403_through_real_broker(self, async_client):
        # No mock: exercise the real OBOBroker -> authorize_scope path. With an
        # empty policy (fail-closed default) every scope is unknown, so the
        # request is denied before IBM Verify is ever contacted.
        from verify import authorization

        authorization.set_scope_policy({})
        subject = jwt.encode(
            {"sub": "user", "groups": ["admin"], "exp": int(time.time()) + 3600},
            "secret",
            algorithm="HS256",
        )
        actor = jwt.encode(
            {"agent_id": "obo-smoke", "exp": int(time.time()) + 3600},
            "secret",
            algorithm="HS256",
        )
        async with async_client as client:
            resp = await client.post(
                "/v1/identity/obo-token",
                json={"subject_token": subject, "actor_token": actor, "scope": "records.read"},
            )

        assert resp.status_code == 403

    async def test_missing_subject_token_returns_422(self, async_client):
        async with async_client as client:
            resp = await client.post("/v1/identity/obo-token", json={"actor_token": "eyJ.actor"})
        assert resp.status_code == 422

    async def test_missing_actor_token_returns_422(self, async_client):
        async with async_client as client:
            resp = await client.post("/v1/identity/obo-token", json={"subject_token": "eyJ.subject"})
        assert resp.status_code == 422

    async def test_missing_scope_returns_422(self, async_client):
        async with async_client as client:
            resp = await client.post(
                "/v1/identity/obo-token",
                json={"subject_token": "eyJ.subject", "actor_token": "eyJ.actor"},
            )
        assert resp.status_code == 422

    async def test_empty_scope_returns_422(self, async_client):
        async with async_client as client:
            resp = await client.post(
                "/v1/identity/obo-token",
                json={
                    "subject_token": "eyJ.subject",
                    "actor_token": "eyJ.actor",
                    "scope": "",
                },
            )
        assert resp.status_code == 422
