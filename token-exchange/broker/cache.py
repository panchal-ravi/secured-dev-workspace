import hashlib
import threading
import time

import jwt
from cachetools import TTLCache

from config.settings import settings
from exceptions.errors import CacheError
from app_logging.logger import get_logger

logger = get_logger(__name__)

SAFETY_BUFFER_SECONDS = 30


def _make_cache_key(subject_token: str, cache_slot: str) -> str:
    """Return sha256(subject_token + cache_slot) as hex — never store raw tokens.

    ``cache_slot`` is a discriminator composed by the caller (the OBO broker
    uses ``"<actor_token>|<normalized_scope>"``) so the same subject paired with
    a different actor or scope never shares a cache entry.
    """
    return hashlib.sha256(f"{subject_token}{cache_slot}".encode()).hexdigest()


def _is_token_valid(token: str) -> bool:
    """Return True if *token* is at least SAFETY_BUFFER_SECONDS from expiry."""
    try:
        claims = jwt.decode(token, options={"verify_signature": False})
        exp: int = claims["exp"]
        return time.time() < exp - SAFETY_BUFFER_SECONDS
    except Exception:
        return False


class TokenCache:
    """Thread-safe TTL cache for issued tokens.

    Wraps :class:`cachetools.TTLCache` with a :class:`threading.Lock` and
    performs JWT expiry validation before returning a cached entry.
    """

    def __init__(self, maxsize: int | None = None, ttl: int | None = None) -> None:
        self._cache: TTLCache = TTLCache(
            maxsize=maxsize or settings.cache_maxsize,
            ttl=ttl or settings.cache_ttl,
        )
        self._lock = threading.Lock()

    def get(self, subject_token: str, cache_slot: str) -> str | None:
        """Return a cached token if present and still valid, otherwise None."""
        key = _make_cache_key(subject_token, cache_slot)
        try:
            with self._lock:
                token: str | None = self._cache.get(key)
        except Exception as exc:
            raise CacheError("Cache read failed") from exc

        if token is None:
            return None

        if _is_token_valid(token):
            return token

        # Token in cache is near/past expiry — evict it proactively.
        logger.debug("cache_token_near_expiry_evicted", cache_slot=cache_slot)
        self.delete(subject_token, cache_slot)
        return None

    def set(self, subject_token: str, cache_slot: str, token: str) -> None:
        """Store *token* under the hashed key."""
        key = _make_cache_key(subject_token, cache_slot)
        try:
            with self._lock:
                self._cache[key] = token
        except Exception as exc:
            raise CacheError("Cache write failed") from exc

    def delete(self, subject_token: str, cache_slot: str) -> None:
        key = _make_cache_key(subject_token, cache_slot)
        with self._lock:
            self._cache.pop(key, None)
