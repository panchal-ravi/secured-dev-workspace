from app_logging.logger import get_logger
from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict


logger = get_logger(__name__)


class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_prefix="TOKEN_EXCHANGE_",
        case_sensitive=False,
        env_file=".env",
        extra="ignore",
    )

    # IBM Verify OBO token exchange settings.
    verify_base_url: str = ""
    obo_client_id: str = ""
    # Confidential client secret for the agent-token-exchange app. Required: the
    # service fails fast at startup if it is missing or empty (IBM Verify
    # authenticates the token-exchange grant with client_secret_post — client_id
    # + client_secret in the request body).
    obo_client_secret: str = Field(min_length=1)

    # Path to the JSON scope -> allowed-groups policy, rendered into the
    # container by the Nomad template stanza. Loaded at startup; a missing or
    # malformed file aborts boot (fail closed — see verify.authorization). The
    # empty default keeps Settings constructible in unit tests that don't
    # exercise policy loading; deployment always sets it.
    scope_policy_file: str = ""

    cache_ttl: int = 3600       # seconds; also the TTLCache eviction window
    cache_maxsize: int = 1024   # max number of cached tokens

    log_level: str = "INFO"

    def log_configured_values(self) -> None:
        values = self.model_dump()
        if values.get("obo_client_secret"):
            values["obo_client_secret"] = "***"
        logger.info("settings_loaded", **values)


settings = Settings()
