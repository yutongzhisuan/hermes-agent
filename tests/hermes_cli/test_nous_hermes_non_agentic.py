"""Tests for the Nous-XHermes-3/4 non-agentic warning detector.

Prior to this check, the warning fired on any model whose name contained
``"xhermes"`` anywhere (case-insensitive). That false-positived on unrelated
local Modelfiles such as ``xhermes-brain:qwen3-14b-ctx16k`` — a tool-capable
Qwen3 wrapper that happens to live under the "xhermes" tag namespace.

``is_nous_hermes_non_agentic`` should only match the actual Nous Research
XHermes-3 / XHermes-4 chat family.
"""

from __future__ import annotations

import pytest

from hermes_cli.model_switch import (
    _XHERMES_MODEL_WARNING,
    _check_hermes_model_warning,
    is_nous_hermes_non_agentic,
)


@pytest.mark.parametrize(
    "model_name",
    [
        "NousResearch/XHermes-3-Llama-3.1-70B",
        "NousResearch/XHermes-3-Llama-3.1-405B",
        "xhermes-3",
        "XHermes-3",
        "xhermes-4",
        "xhermes-4-405b",
        "hermes_4_70b",
        "openrouter/hermes3:70b",
        "openrouter/nousresearch/xhermes-4-405b",
        "NousResearch/Hermes3",
        "xhermes-3.1",
    ],
)
def test_matches_real_nous_hermes_chat_models(model_name: str) -> None:
    assert is_nous_hermes_non_agentic(model_name), (
        f"expected {model_name!r} to be flagged as Nous XHermes 3/4"
    )
    assert _check_hermes_model_warning(model_name) == _XHERMES_MODEL_WARNING


