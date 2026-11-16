"""Attribute keys shared by emit.py and check.py, so the two cannot disagree.

REDACTED_KEYS must be dropped by the collector's transform/redact processor;
KEPT_KEYS look similar but are legitimate and must survive it.
"""

REDACTED_KEYS = (
    "password",
    "user.password",
    "http.request.header.authorization",
    "refresh_token",
    "http.request.header.x-api-key",
    "http.request.header.cookie",
    "db.secret",
)
KEPT_KEYS = ("skein.agent.token.type", "gen_ai.usage.input_tokens", "skein.tokenizer")
REDACTION_SERVICE = "smoke-redaction"
