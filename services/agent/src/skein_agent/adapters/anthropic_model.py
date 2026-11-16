"""The production model: langchain-anthropic's ChatAnthropic, configured by
environment (``ANTHROPIC_API_KEY``, ``SKEIN_AGENT_MODEL``).

``langchain_anthropic`` is imported only when a model is created, so tests
and the scripted evals never touch it.
"""

from __future__ import annotations

from langchain_core.language_models import BaseChatModel


class AnthropicModelProvider:
    """ModelProvider backed by the Anthropic Messages API."""

    def __init__(self, *, api_key: str, model: str, max_tokens: int = 4096, timeout_s: float = 60.0) -> None:
        if not api_key:
            raise ValueError("ANTHROPIC_API_KEY is required for the Anthropic model")
        self._api_key = api_key
        self._model = model
        self._max_tokens = max_tokens
        self._timeout = timeout_s

    @property
    def model_name(self) -> str:
        """See ModelProvider.model_name."""
        return self._model

    def chat_model(self) -> BaseChatModel:
        """See ModelProvider.chat_model."""
        from langchain_anthropic import ChatAnthropic

        return ChatAnthropic(
            model_name=self._model,
            api_key=self._api_key,
            max_tokens_to_sample=self._max_tokens,
            timeout=self._timeout,
            max_retries=2,
            streaming=True,
            stream_usage=True,
        )
