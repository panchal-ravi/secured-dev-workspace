"""Tests for settings loading from .env and environment variables.

``conftest.py`` sets the ``TOKEN_EXCHANGE_*`` env vars process-wide so the
module-level ``Settings()`` is constructible; the .env-precedence tests below
clear those vars first so the .env file is actually exercised.
"""
from unittest.mock import patch

import pytest
from pydantic import ValidationError

from config.settings import Settings

_PREFIXED = (
    "TOKEN_EXCHANGE_VERIFY_BASE_URL",
    "TOKEN_EXCHANGE_OBO_CLIENT_ID",
    "TOKEN_EXCHANGE_OBO_CLIENT_SECRET",
    "TOKEN_EXCHANGE_SCOPE_POLICY_FILE",
)


def _clear_env(monkeypatch):
    for name in _PREFIXED:
        monkeypatch.delenv(name, raising=False)


class TestSettings:
    def test_reads_values_from_dotenv_file(self, tmp_path, monkeypatch):
        dotenv_path = tmp_path / ".env"
        dotenv_path.write_text(
            "TOKEN_EXCHANGE_VERIFY_BASE_URL=https://tenant.verify.ibm.com\n"
            "TOKEN_EXCHANGE_OBO_CLIENT_ID=dotenv-client-id\n"
            "TOKEN_EXCHANGE_OBO_CLIENT_SECRET=dotenv-client-secret\n"
            "TOKEN_EXCHANGE_SCOPE_POLICY_FILE=/local/scope-policy.json\n"
        )
        monkeypatch.chdir(tmp_path)
        _clear_env(monkeypatch)

        settings = Settings()

        assert settings.verify_base_url == "https://tenant.verify.ibm.com"
        assert settings.obo_client_id == "dotenv-client-id"
        assert settings.obo_client_secret == "dotenv-client-secret"
        assert settings.scope_policy_file == "/local/scope-policy.json"

    def test_environment_variables_override_dotenv_values(self, tmp_path, monkeypatch):
        dotenv_path = tmp_path / ".env"
        dotenv_path.write_text(
            "TOKEN_EXCHANGE_VERIFY_BASE_URL=https://dotenv.verify.ibm.com\n"
            "TOKEN_EXCHANGE_OBO_CLIENT_ID=dotenv-client-id\n"
            "TOKEN_EXCHANGE_OBO_CLIENT_SECRET=dotenv-client-secret\n"
        )
        monkeypatch.chdir(tmp_path)
        _clear_env(monkeypatch)
        monkeypatch.setenv(
            "TOKEN_EXCHANGE_VERIFY_BASE_URL",
            "https://env.verify.ibm.com",
        )

        settings = Settings()

        assert settings.verify_base_url == "https://env.verify.ibm.com"
        assert settings.obo_client_id == "dotenv-client-id"

    def test_missing_client_secret_raises(self, tmp_path, monkeypatch):
        dotenv_path = tmp_path / ".env"
        dotenv_path.write_text(
            "TOKEN_EXCHANGE_VERIFY_BASE_URL=https://tenant.verify.ibm.com\n"
            "TOKEN_EXCHANGE_OBO_CLIENT_ID=dotenv-client-id\n"
        )
        monkeypatch.chdir(tmp_path)
        _clear_env(monkeypatch)

        with pytest.raises(ValidationError):
            Settings()

    def test_log_configured_values_masks_secret(self):
        settings = Settings(
            verify_base_url="https://tenant.verify.ibm.com",
            obo_client_id="client-id",
            obo_client_secret="super-secret",
            scope_policy_file="/local/scope-policy.json",
            cache_ttl=120,
            cache_maxsize=50,
            log_level="DEBUG",
        )

        with patch("config.settings.logger") as mock_logger:
            settings.log_configured_values()

        mock_logger.info.assert_called_once_with(
            "settings_loaded",
            verify_base_url="https://tenant.verify.ibm.com",
            obo_client_id="client-id",
            obo_client_secret="***",
            scope_policy_file="/local/scope-policy.json",
            cache_ttl=120,
            cache_maxsize=50,
            log_level="DEBUG",
        )
