import requests
import structlog
from fastapi import APIRouter, Request
from fastapi.responses import JSONResponse

from exceptions.errors import (
    CacheError,
    VerifyAuthenticationError,
    VerifyAuthorizationError,
    VerifyTokenExchangeError,
)
from app_logging.logger import get_logger
from models.schemas import OBOTokenRequest, OBOTokenResponse
from verify.obo_broker import OBOBroker, claim_from_token

router = APIRouter()
logger = get_logger(__name__)

# Single shared broker instance (cache is inside the broker).
_obo_broker = OBOBroker()


@router.post("/v1/identity/obo-token", response_model=OBOTokenResponse)
async def exchange_obo_token(
    request: Request, body: OBOTokenRequest
) -> OBOTokenResponse:
    """Exchange subject_token + actor_token for an IBM Verify OBO access token."""
    structlog.contextvars.bind_contextvars(
        preferred_username=claim_from_token(body.subject_token, "preferred_username"),
        agent_id=claim_from_token(body.actor_token, "agent_id"),
    )
    try:
        result = _obo_broker.exchange_obo_token(
            subject_token=body.subject_token,
            actor_token=body.actor_token,
            scope=body.scope,
        )
        return OBOTokenResponse(
            access_token=result.access_token,
            cached=result.cached,
        )

    except VerifyAuthorizationError as exc:
        logger.warning("obo_token_exchange_authz_denied", error=str(exc))
        return JSONResponse(status_code=403, content={"detail": str(exc)})

    except VerifyAuthenticationError as exc:
        logger.warning("obo_token_exchange_auth_failure", error=str(exc))
        return JSONResponse(status_code=401, content={"detail": str(exc)})

    except (VerifyTokenExchangeError, CacheError) as exc:
        logger.error("obo_token_exchange_internal_error", error=str(exc))
        return JSONResponse(status_code=500, content={"detail": str(exc)})

    except requests.exceptions.ConnectionError as exc:
        logger.error("verify_unavailable", error=str(exc))
        return JSONResponse(
            status_code=503, content={"detail": "IBM Verify is unavailable"}
        )

    except Exception as exc:
        logger.exception("obo_token_exchange_unexpected_error", error=str(exc))
        return JSONResponse(
            status_code=500, content={"detail": "Internal server error"}
        )


@router.get("/healthz")
async def health() -> dict:
    """Liveness probe."""
    return {"status": "ok"}
