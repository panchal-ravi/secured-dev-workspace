"""Unit tests for the IBMVerifyClient HTTP layer."""
from unittest.mock import MagicMock, patch

from verify.verify_client import IBMVerifyClient


def _ok_response() -> MagicMock:
    response = MagicMock()
    response.status_code = 200
    response.ok = True
    response.json.return_value = {"access_token": "obo-access-token"}
    return response


class TestExchangeOBOToken:
    def test_sends_rfc8693_form_fields(self):
        client = IBMVerifyClient(
            base_url="https://tenant.verify.ibm.com",
            client_id="the-client-id",
            client_secret="the-client-secret",
        )

        with patch("verify.verify_client.requests.post", return_value=_ok_response()) as post:
            client.exchange_obo_token("subject-tok", "actor-tok", "users.read")

        sent = post.call_args.kwargs["data"]
        assert sent["client_id"] == "the-client-id"
        assert sent["client_secret"] == "the-client-secret"
        assert sent["grant_type"] == "urn:ietf:params:oauth:grant-type:token-exchange"
        assert sent["subject_token"] == "subject-tok"
        assert sent["actor_token"] == "actor-tok"
        assert sent["actor_token_type"] == "urn:demo:token-type:vault-identity-jwt"
        assert sent["scope"] == "users.read"

    def test_debug_log_redacts_secrets(self):
        client = IBMVerifyClient(
            base_url="https://tenant.verify.ibm.com",
            client_id="the-client-id",
            client_secret="the-client-secret",
        )

        with patch("verify.verify_client.requests.post", return_value=_ok_response()), patch(
            "verify.verify_client.logger"
        ) as mock_logger:
            client.exchange_obo_token("subject-tok", "actor-tok", "users.read")

        logged_payload = mock_logger.debug.call_args.kwargs["payload"]
        assert logged_payload["client_secret"] == "<redacted>"
        assert logged_payload["subject_token"] == "<redacted>"
        assert logged_payload["actor_token"] == "<redacted>"
        assert logged_payload["client_id"] == "the-client-id"
