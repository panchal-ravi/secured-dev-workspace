"""Unit tests for TokenCache."""
import time

import jwt

from broker.cache import TokenCache, _make_cache_key


def _make_jwt(exp_offset: int) -> str:
    """Return a minimal JWT with exp = now + exp_offset (no real signature)."""
    payload = {"sub": "test", "exp": int(time.time()) + exp_offset}
    return jwt.encode(payload, "secret", algorithm="HS256")


class TestMakeCacheKey:
    def test_deterministic(self):
        assert _make_cache_key("tok", "slot") == _make_cache_key("tok", "slot")

    def test_different_inputs_differ(self):
        assert _make_cache_key("tok1", "slot") != _make_cache_key("tok2", "slot")

    def test_does_not_contain_raw_token(self):
        key = _make_cache_key("my-secret-token", "slot")
        assert "my-secret-token" not in key


class TestTokenCache:
    def setup_method(self):
        self.cache = TokenCache(maxsize=10, ttl=60)

    def test_miss_returns_none(self):
        assert self.cache.get("tok", "slot") is None

    def test_set_and_get_valid_token(self):
        token = _make_jwt(exp_offset=3600)
        self.cache.set("tok", "slot", token)
        assert self.cache.get("tok", "slot") == token

    def test_near_expiry_token_is_evicted(self):
        # exp = now + 10 seconds — within the 30s safety buffer
        token = _make_jwt(exp_offset=10)
        self.cache.set("tok", "slot", token)
        assert self.cache.get("tok", "slot") is None

    def test_already_expired_token_is_evicted(self):
        token = _make_jwt(exp_offset=-1)
        self.cache.set("tok", "slot", token)
        assert self.cache.get("tok", "slot") is None

    def test_delete_removes_entry(self):
        token = _make_jwt(exp_offset=3600)
        self.cache.set("tok", "slot", token)
        self.cache.delete("tok", "slot")
        assert self.cache.get("tok", "slot") is None

    def test_different_slots_stored_independently(self):
        t1 = _make_jwt(exp_offset=3600)
        t2 = _make_jwt(exp_offset=3600)
        self.cache.set("tok", "slot-a", t1)
        self.cache.set("tok", "slot-b", t2)
        assert self.cache.get("tok", "slot-a") == t1
        assert self.cache.get("tok", "slot-b") == t2
