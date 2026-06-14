from contextlib import asynccontextmanager
import time
from typing import AsyncGenerator
from uuid import uuid4

import structlog
from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse

from api.routes import router
from config.settings import settings
from app_logging.logger import bind_request_context, clear_request_id, configure_logging
from verify.authorization import load_scope_policy, set_scope_policy

configure_logging(settings.log_level)


@asynccontextmanager
async def lifespan(app: FastAPI) -> AsyncGenerator[None, None]:
    settings.log_configured_values()
    # Load the scope -> allowed-groups authorization policy from disk. A missing
    # or malformed file raises here and aborts startup — the service fails
    # closed rather than booting with no enforceable policy.
    set_scope_policy(load_scope_policy(settings.scope_policy_file))
    structlog.get_logger(__name__).info("token_exchange_starting")
    yield
    structlog.get_logger(__name__).info("token_exchange_stopping")


app = FastAPI(
    title="Token Exchange",
    description="Exchanges a user access token + Vault actor JWT for an IBM Verify OBO access token.",
    version="0.1.0",
    lifespan=lifespan,
)


@app.middleware("http")
async def correlation_id_middleware(request: Request, call_next):
    """Bind request metadata to all log entries for the current request."""
    incoming_request_id = request.headers.get("X-Request-ID")
    request_id = incoming_request_id or str(uuid4())
    start = time.monotonic()
    bind_request_context(request, request_id)

    try:
        response = await call_next(request)
        duration_ms = int((time.monotonic() - start) * 1000)
        # Per-request access traces are useful for deep-dive debugging only;
        # keep them off by default so the OBO exchange line is the dominant
        # signal at INFO.
        structlog.get_logger("api.access").debug(
            "request_completed",
            status_code=response.status_code,
            duration_ms=duration_ms,
        )
        if incoming_request_id is not None:
            response.headers["X-Request-ID"] = request_id
        return response
    finally:
        clear_request_id()


@app.exception_handler(Exception)
async def unhandled_exception_handler(request: Request, exc: Exception) -> JSONResponse:
    structlog.get_logger(__name__).exception("unhandled_exception", error=str(exc))
    return JSONResponse(status_code=500, content={"detail": "Internal server error"})


app.include_router(router)
