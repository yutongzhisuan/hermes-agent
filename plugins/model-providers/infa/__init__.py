"""INFA consumer-session provider profile."""

from providers import register_provider
from providers.base import ProviderProfile

infa = ProviderProfile(
    name="infa",
    aliases=("infa-oauth", "xhermes-infa"),
    display_name="INFA",
    description="INFA consumer session (email login, JWT + DPoP chat completions)",
    api_mode="chat_completions",
    env_vars=("INFA_GATEWAY_BASE_URL", "INFA_PLATFORM_BASE_URL"),
    base_url="",
    auth_type="oauth_external",
    supports_health_check=False,
)

register_provider(infa)
