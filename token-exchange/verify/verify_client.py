import requests

from config.settings import settings
from exceptions.errors import (
    VerifyAuthenticationError,
    VerifyTokenExchangeError,
)
from app_logging.logger import get_logger

logger = get_logger(__name__)

_GRANT_TYPE = "urn:ietf:params:oauth:grant-type:token-exchange"
_REQUESTED_TOKEN_TYPE = "urn:ietf:params:oauth:token-type:access_token"
_SUBJECT_TOKEN_TYPE = "urn:ietf:params:oauth:token-type:access_token"
_ACTOR_TOKEN_TYPE = "urn:demo:token-type:vault-identity-jwt"


class IBMVerifyClient:
    """HTTP client for IBM Verify on-behalf-of (OBO) token exchange.

    Performs an RFC 8693 token exchange against the IBM Verify token endpoint.
    Configuration is read from :mod:`config.settings` at instantiation time.
    """

    def __init__(
        self,
        base_url: str | None = None,
        client_id: str | None = None,
        client_secret: str | None = None,
    ) -> None:
        self._token_url = (
            f"{(base_url or settings.verify_base_url).rstrip('/')}/oauth2/token"
        )
        self._client_id = client_id or settings.obo_client_id
        self._client_secret = client_secret or settings.obo_client_secret

    def exchange_obo_token(
        self, subject_token: str, actor_token: str, scope: str
    ) -> dict:
        """Exchange *subject_token* + *actor_token* for an IBM Verify access token.

        Args:
            subject_token: The caller's access token (JWT) to act on behalf of.
            actor_token:   The Vault Identity JWT that identifies the acting service.
            scope:         Space-separated OAuth scopes to request on the OBO token.

        Returns:
            Parsed JSON response dict from IBM Verify (contains ``access_token``,
            ``token_type``, ``expires_in``, etc.).

        Raises:
            VerifyAuthenticationError:  IBM Verify returned 401.
            VerifyTokenExchangeError:   Any other HTTP or connection failure.
        """

        payload = {
            "client_id": self._client_id,
            "client_secret": self._client_secret,
            "grant_type": _GRANT_TYPE,
            "requested_token_type": _REQUESTED_TOKEN_TYPE,
            "subject_token_type": _SUBJECT_TOKEN_TYPE,
            "subject_token": subject_token,
            "actor_token_type": _ACTOR_TOKEN_TYPE,
            "actor_token": actor_token,
            "scope": scope,
        }

        logger.debug(
            "verify_obo_token_exchange_payload",
            payload={
                k: (
                    v
                    if k not in {"subject_token", "actor_token", "client_secret"}
                    else "<redacted>"
                )
                for k, v in payload.items()
            },
        )

        try:
            response = requests.post(
                self._token_url,
                data=payload,
                headers={"Content-Type": "application/x-www-form-urlencoded"},
                timeout=10,
            )
        except requests.exceptions.RequestException as exc:
            raise VerifyTokenExchangeError(
                f"Network error contacting IBM Verify: {exc}"
            ) from exc

        if response.status_code == 401:
            raise VerifyAuthenticationError(
                "IBM Verify rejected the OBO request: unauthorized"
            )

        if not response.ok:
            logger.warning(
                "verify_obo_http_error",
                status_code=response.status_code,
                response_body=response.text,
            )
            raise VerifyTokenExchangeError(
                f"IBM Verify OBO exchange failed with HTTP {response.status_code}"
            )

        try:
            return response.json()
        except Exception as exc:
            raise VerifyTokenExchangeError(
                "IBM Verify returned a non-JSON response"
            ) from exc
