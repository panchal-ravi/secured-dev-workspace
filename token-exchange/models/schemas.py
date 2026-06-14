from pydantic import BaseModel, Field


class OBOTokenRequest(BaseModel):
    subject_token: str = Field(
        ..., min_length=1, description="Caller's access token (JWT) to act on behalf of"
    )
    actor_token: str = Field(
        ..., min_length=1, description="Vault Identity JWT identifying the actor"
    )
    scope: str = Field(
        ...,
        min_length=1,
        description="Space-separated OAuth scopes (RFC 8693 'scope' parameter)",
    )


class OBOTokenResponse(BaseModel):
    access_token: str = Field(
        ..., description="IBM Verify access token issued on behalf of the subject"
    )
    cached: bool = Field(..., description="Whether the token was served from cache")
