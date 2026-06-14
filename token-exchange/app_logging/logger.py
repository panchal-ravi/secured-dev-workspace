import logging
import socket
import sys
from typing import Any

import structlog
from starlette.requests import Request
from structlog.processors import CallsiteParameter, CallsiteParameterAdder

SERVICE_NAME = "token-exchange"
HOSTNAME = socket.gethostname()

try:
    HOST_IP = socket.gethostbyname(HOSTNAME)
except socket.gaierror:
    HOST_IP = None


def _add_standard_fields(
    _: logging.Logger, __: str, event_dict: structlog.typing.EventDict
) -> structlog.typing.EventDict:
    event_dict.setdefault("service", SERVICE_NAME)
    event_dict.setdefault("hostname", HOSTNAME)
    if HOST_IP is not None:
        event_dict.setdefault("host_ip", HOST_IP)
    return event_dict


def _add_message_field(
    _: logging.Logger, __: str, event_dict: structlog.typing.EventDict
) -> structlog.typing.EventDict:
    """Ensure every record has a ``message`` field (mirrors ``event`` when absent)."""
    if "message" not in event_dict:
        event = event_dict.get("event")
        if event is not None:
            event_dict["message"] = event
    return event_dict


def _prepend_identity_to_message(
    _: logging.Logger, __: str, event_dict: structlog.typing.EventDict
) -> structlog.typing.EventDict:
    """Prepend ``user`` and ``agent`` identifiers to the ``message`` field."""
    preferred_username = event_dict.get("preferred_username")
    agent_id = event_dict.get("agent_id")
    message = event_dict.get("message")
    if not isinstance(message, str):
        return event_dict
    identity_parts: list[str] = []
    if preferred_username:
        identity_parts.append(f"user={preferred_username}")
    if agent_id:
        identity_parts.append(f"agent={agent_id}")
    if identity_parts:
        event_dict["message"] = f"[{' '.join(identity_parts)}] {message}"
    return event_dict


def _add_uvicorn_access_fields(
    _: logging.Logger, __: str, event_dict: structlog.typing.EventDict
) -> structlog.typing.EventDict:
    if (
        event_dict.get("logger") == "uvicorn.access"
        and isinstance(event_dict.get("positional_args"), tuple)
        and len(event_dict["positional_args"]) == 5
    ):
        client_addr, http_method, http_path, http_version, status_code = event_dict.pop(
            "positional_args"
        )
        event_dict.setdefault("client_addr", client_addr)
        event_dict.setdefault("http_method", http_method)
        event_dict.setdefault("http_path", http_path)
        event_dict.setdefault("http_version", http_version)
        event_dict.setdefault("status_code", status_code)
    return event_dict


def _shared_processors() -> list[Any]:
    return [
        structlog.contextvars.merge_contextvars,
        structlog.stdlib.add_log_level,
        structlog.stdlib.add_logger_name,
        structlog.processors.TimeStamper(fmt="iso", key="timestamp"),
        CallsiteParameterAdder(
            {
                CallsiteParameter.MODULE,
                CallsiteParameter.FUNC_NAME,
                CallsiteParameter.LINENO,
                CallsiteParameter.PROCESS,
                CallsiteParameter.THREAD_NAME,
            }
        ),
        structlog.processors.StackInfoRenderer(),
        _add_standard_fields,
        _add_message_field,
        _prepend_identity_to_message,
    ]


def _configure_stdlib_logger(name: str, handler: logging.Handler, level: int) -> None:
    logger = logging.getLogger(name)
    logger.handlers.clear()
    logger.addHandler(handler)
    logger.setLevel(level)
    logger.propagate = False


class _DowngradeToDebugFilter(logging.Filter):
    """Rewrite every record on this logger from its native level to DEBUG.

    uvicorn emits per-request access lines at INFO; we want them gone from
    the default INFO output. Downgrading instead of dropping keeps them
    available when the operator turns log level up to DEBUG.
    """

    def filter(self, record: logging.LogRecord) -> bool:
        record.levelno = logging.DEBUG
        record.levelname = "DEBUG"
        return record.levelno >= logging.getLogger().getEffectiveLevel()


def configure_logging(log_level: str = "INFO") -> None:
    """Configure JSON logging with standard fields for Loki ingestion."""
    resolved_level = getattr(logging, log_level.upper(), logging.INFO)
    shared_processors = _shared_processors()
    formatter = structlog.stdlib.ProcessorFormatter(
        foreign_pre_chain=shared_processors,
        pass_foreign_args=True,
        processors=[
            _add_uvicorn_access_fields,
            structlog.stdlib.ProcessorFormatter.remove_processors_meta,
            structlog.processors.JSONRenderer(),
        ],
    )

    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(formatter)

    root_logger = logging.getLogger()
    root_logger.handlers.clear()
    root_logger.addHandler(handler)
    root_logger.setLevel(resolved_level)

    for logger_name in ("uvicorn", "uvicorn.error", "uvicorn.access"):
        _configure_stdlib_logger(logger_name, handler, resolved_level)

    # uvicorn.access records always come in at INFO; downgrade to DEBUG so
    # they don't dominate INFO output. The api.access logger is already at
    # DEBUG via the structlog call site.
    access_logger = logging.getLogger("uvicorn.access")
    if not any(isinstance(f, _DowngradeToDebugFilter) for f in access_logger.filters):
        access_logger.addFilter(_DowngradeToDebugFilter())

    structlog.configure(
        processors=[
            structlog.stdlib.filter_by_level,
            *shared_processors,
            structlog.stdlib.ProcessorFormatter.wrap_for_formatter,
        ],
        wrapper_class=structlog.stdlib.BoundLogger,
        context_class=dict,
        logger_factory=structlog.stdlib.LoggerFactory(),
        cache_logger_on_first_use=True,
    )


def get_logger(name: str) -> structlog.stdlib.BoundLogger:
    return structlog.get_logger(name)


def bind_request_id(request_id: str) -> None:
    """Bind the caller-provided request ID to the current context."""
    structlog.contextvars.bind_contextvars(request_id=request_id)


def bind_request_context(request: Request, request_id: str) -> None:
    forwarded_for = request.headers.get("x-forwarded-for")
    client_ip = (
        forwarded_for.split(",")[0].strip()
        if forwarded_for
        else request.client.host if request.client else None
    )
    context = {
        "request_id": request_id,
        "http_method": request.method,
        "http_path": request.url.path,
        "http_scheme": request.url.scheme,
    }
    if client_ip:
        context["client_ip"] = client_ip
    user_agent = request.headers.get("user-agent")
    if user_agent:
        context["user_agent"] = user_agent
    structlog.contextvars.bind_contextvars(**context)


def clear_request_id() -> None:
    structlog.contextvars.clear_contextvars()
