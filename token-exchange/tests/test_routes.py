"""Transport-level tests: correlation-id middleware + healthz."""
import pytest
from httpx import ASGITransport, AsyncClient

from api.main import app


@pytest.fixture()
def async_client():
    transport = ASGITransport(app=app)
    return AsyncClient(transport=transport, base_url="http://test")


class TestTransport:
    async def test_request_id_header_is_preserved(self, async_client):
        async with async_client as client:
            resp = await client.get("/healthz", headers={"X-Request-ID": "req-123"})

        assert resp.status_code == 200
        assert resp.headers["X-Request-ID"] == "req-123"

    async def test_request_id_header_is_not_added_when_missing(self, async_client):
        async with async_client as client:
            resp = await client.get("/healthz")

        assert resp.status_code == 200
        assert "X-Request-ID" not in resp.headers

    async def test_healthz(self, async_client):
        async with async_client as client:
            resp = await client.get("/healthz")
        assert resp.status_code == 200
        assert resp.json() == {"status": "ok"}
