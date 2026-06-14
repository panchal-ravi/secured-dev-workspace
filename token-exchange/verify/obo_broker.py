import time
from dataclasses import dataclass

import jwt
import tenacity
import tenacity.stop
import tenacity.wait

from broker.cache import TokenCache
from exceptions.errors import VerifyTokenExchangeError
from app_logging.logger import get_logger
from verify.authorization import authorize_scope
from verify.verify_client import IBMVerifyClient

logger = get_logger(__name__)


def claim_from_token(token: str, claim: str) -> str | None:
    """Return *claim* from *token* by decoding the JWT without verification."""
    try:
        payload = jwt.decode(token, options={"verify_signature": False})
    except Exception:
        return None
    value = payload.get(claim) if isinstance(payload, dict) else None
    return value if isinstance(value, str) else None


@dataclass
class OBOTokenResult:
    access_token: str
    cached: bool


class OBOBroker:
    """Broker that performs an IBM Verify on-behalf-of (OBO) token exchange.

    Public API:
        exchange_obo_token(subject_token, actor_token, scope) -> OBOTokenResult
    """

    def __init__(
        self,
        verify_client: IBMVerifyClient | None = None,
        cache: TokenCache | None = None,
    ) -> None:
        self._client = verify_client or IBMVerifyClient()
        self._cache = cache or TokenCache()

    def exchange_obo_token(
        self, subject_token: str, actor_token: str, scope: str
    ) -> OBOTokenResult:
        """Return an IBM Verify access token on behalf of *subject_token*.

        Checks the cache first using a hashed key derived from both input tokens
        and the requested scope set. On a cache miss, calls IBM Verify with
        automatic retry (3 attempts, exponential backoff) on transient failures.

        Args:
            subject_token: The caller's access token (JWT).
            actor_token:   The Vault Identity JWT acting on behalf of the subject.
            scope:         Space-separated OAuth scopes to request on the OBO token.

        Returns:
            :class:`OBOTokenResult` with the access token and cache flag.

        Raises:
            VerifyAuthorizationError:   Caller's groups don't entitle the requested scope.
            VerifyAuthenticationError:  IBM Verify rejected the request.
            VerifyTokenExchangeError:   IBM Verify could not complete the exchange.
            CacheError:                 Unexpected cache failure.
        """
        start = time.monotonic()
        authorize_scope(subject_token, scope)
        preferred_username = claim_from_token(subject_token, "preferred_username")
        agent_id = claim_from_token(actor_token, "agent_id")

        normalized_scope = _normalize_scope(scope)
        # Compose actor_token + normalized scope into the second cache slot so the
        # same subject+actor pair with different scopes never share a cache entry.
        cache_slot = f"{actor_token}|{normalized_scope}"

        cached_token = self._cache.get(subject_token, cache_slot)
        if cached_token:
            duration_ms = int((time.monotonic() - start) * 1000)
            logger.info(
                "verify_obo_token_exchange",
                message=f"OBO token exchange (cache hit) for scope={normalized_scope!r}",
                preferred_username=preferred_username,
                agent_id=agent_id,
                scope=normalized_scope,
                cache_hit=True,
                duration_ms=duration_ms,
            )
            return OBOTokenResult(access_token=cached_token, cached=True)

        response_data = self._fetch_with_retry(
            subject_token, actor_token, normalized_scope
        )
        access_token: str = response_data["access_token"]
        self._cache.set(subject_token, cache_slot, access_token)

        duration_ms = int((time.monotonic() - start) * 1000)
        logger.info(
            "verify_obo_token_exchange",
            message=f"OBO token exchange (issued by IBM Verify) for scope={normalized_scope!r}",
            preferred_username=preferred_username,
            agent_id=agent_id,
            scope=normalized_scope,
            cache_hit=False,
            duration_ms=duration_ms,
        )
        return OBOTokenResult(access_token=access_token, cached=False)

    @tenacity.retry(
        retry=tenacity.retry_if_exception_type(VerifyTokenExchangeError),
        stop=tenacity.stop.stop_after_attempt(3),
        wait=tenacity.wait.wait_exponential(multiplier=0.5, min=0.5, max=4),
        reraise=True,
    )
    def _fetch_with_retry(
        self, subject_token: str, actor_token: str, scope: str
    ) -> dict:
        return self._client.exchange_obo_token(subject_token, actor_token, scope)


def _normalize_scope(scope: str) -> str:
    """Return *scope* with tokens deduped, sorted, and single-space-joined.

    Ensures cache key stability across callers that send the same scope set in
    different orders (e.g. "users.write users.read" == "users.read users.write").
    """
    parts = sorted({tok for tok in scope.split() if tok})
    return " ".join(parts)
