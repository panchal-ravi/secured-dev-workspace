import json
import logging

import structlog
from starlette.requests import Request

from app_logging import logger as logger_module


def _make_request(headers: list[tuple[bytes, bytes]] | None = None) -> Request:
    return Request(
        {
            "type": "http",
            "http_version": "1.1",
            "method": "POST",
            "scheme": "https",
            "path": "/v1/identity/obo-token",
            "raw_path": b"/v1/identity/obo-token",
            "query_string": b"",
            "headers": headers or [],
            "client": ("198.51.100.10", 443),
            "server": ("testserver", 443),
        }
    )


class TestLogging:
    def test_configure_logging_includes_standard_fields(self, capsys):
        logger_module.configure_logging("INFO")

        logger_module.get_logger("tests.logging").info("log_event")

        output = capsys.readouterr().out.strip()
        record = json.loads(output)

        assert record["event"] == "log_event"
        assert record["level"] == "info"
        assert record["logger"] == "tests.logging"
        assert record["service"] == "token-exchange"
        assert record["hostname"] == logger_module.HOSTNAME
        assert "timestamp" in record
        assert "module" in record
        assert "func_name" in record
        assert "lineno" in record
        assert "process" in record
        assert "thread_name" in record
        if logger_module.HOST_IP is not None:
            assert record["host_ip"] == logger_module.HOST_IP

    def test_configure_logging_formats_uvicorn_error_logs_as_json(self, capsys):
        logger_module.configure_logging("INFO")

        logging.getLogger("uvicorn.error").info("server_started")

        output = capsys.readouterr().out.strip()
        record = json.loads(output)

        assert record["event"] == "server_started"
        assert record["logger"] == "uvicorn.error"
        assert record["level"] == "info"

    def test_configure_logging_extracts_uvicorn_access_fields(self, capsys):
        # Access records are downgraded to DEBUG; configure logging at DEBUG
        # so the record is actually emitted, then verify its parsed shape.
        logger_module.configure_logging("DEBUG")

        logging.getLogger("uvicorn.access").info(
            '%s - "%s %s HTTP/%s" %d',
            "203.0.113.10:50000",
            "GET",
            "/healthz",
            "1.1",
            200,
        )

        output = capsys.readouterr().out.strip()
        record = json.loads(output)

        assert record["logger"] == "uvicorn.access"
        # The downgrade filter rewrites the record's level before render.
        assert record["level"] == "debug"
        assert record["client_addr"] == "203.0.113.10:50000"
        assert record["http_method"] == "GET"
        assert record["http_path"] == "/healthz"
        assert record["http_version"] == "1.1"
        assert record["status_code"] == 200

    def test_uvicorn_access_records_are_suppressed_at_info(self, capsys):
        logger_module.configure_logging("INFO")

        logging.getLogger("uvicorn.access").info(
            '%s - "%s %s HTTP/%s" %d',
            "203.0.113.10:50000",
            "GET",
            "/healthz",
            "1.1",
            200,
        )

        # Nothing emitted because the record was downgraded to DEBUG and the
        # root logger is at INFO.
        assert capsys.readouterr().out == ""

    def test_bind_request_context_includes_http_metadata(self):
        structlog.contextvars.clear_contextvars()
        request = _make_request(
            headers=[
                (b"x-forwarded-for", b"203.0.113.10, 10.0.0.1"),
                (b"user-agent", b"pytest"),
            ]
        )

        logger_module.bind_request_context(request, "req-123")

        context = structlog.contextvars.get_contextvars()

        assert context == {
            "request_id": "req-123",
            "http_method": "POST",
            "http_path": "/v1/identity/obo-token",
            "http_scheme": "https",
            "client_ip": "203.0.113.10",
            "user_agent": "pytest",
        }

        logger_module.clear_request_id()
