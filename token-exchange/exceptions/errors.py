class TokenExchangeError(Exception):
    """Base exception for token-exchange service errors."""


class CacheError(TokenExchangeError):
    """Raised on unexpected cache read/write failures."""


class VerifyOBOError(Exception):
    """Base exception for IBM Verify OBO token exchange errors."""


class VerifyAuthenticationError(VerifyOBOError):
    """Raised when IBM Verify rejects the OBO request due to invalid credentials."""


class VerifyTokenExchangeError(VerifyOBOError):
    """Raised when IBM Verify fails to complete the OBO token exchange."""


class VerifyAuthorizationError(VerifyOBOError):
    """Raised when the subject_token's groups don't entitle the requested scope."""
