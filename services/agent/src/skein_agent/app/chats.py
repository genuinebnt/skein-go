"""Chats: CreateChat and ListChats.

A chat belongs to one user in one workspace. Creating one checks that the
user may run the agent there (``ACTION_AGENT_RUN`` on the workspace), asking
identity with the user's own token, so a chat cannot exist where its runs
would be refused. The title is the start of the first message, set when the
first run completes.
"""

from __future__ import annotations

from collections.abc import Sequence

from .. import pb
from ..domain.errors import AgentError, AgentErrorKind, ErrorCode, ServiceError
from ..domain.ids import uuid7
from .ports import Chat, ChatStore, Clock, ServiceClient
from .runs import Principal

MAX_PAGE = 100
DEFAULT_PAGE = 20


class Chats:
    """Use cases for chat sessions."""

    def __init__(self, store: ChatStore, identity: ServiceClient, clock: Clock, *, call_timeout_s: float = 5.0) -> None:
        self._store = store
        self._identity = identity
        self._clock = clock
        self._timeout = call_timeout_s

    async def create(self, principal: Principal, workspace_id: str, note_id: str = "") -> str:
        """Create a chat and return its ID."""
        if not workspace_id:
            raise AgentError(AgentErrorKind.INVALID_ARGUMENT, "workspace_id is required")
        await self._require_agent_access(principal, workspace_id)
        chat = Chat(uuid7(), principal.user_id, workspace_id, note_id, "", int(self._clock.now() * 1000))
        await self._store.create(chat)
        return chat.chat_id

    async def list(
        self, principal: Principal, workspace_id: str, page_size: int, page_token: str
    ) -> tuple[Sequence[Chat], str]:
        """A page of the user's chats and the next page token ("" at the end)."""
        size = DEFAULT_PAGE if page_size == 0 else min(page_size, MAX_PAGE)
        chats = await self._store.list(principal.user_id, workspace_id, limit=size + 1, after=page_token)
        if len(chats) > size:
            return chats[:size], chats[size - 1].chat_id
        return chats, ""

    async def _require_agent_access(self, principal: Principal, workspace_id: str) -> None:
        i = pb.identity_pb2
        request = i.AuthorizeRequest(
            subject=i.Subject(user_id=principal.user_id),
            checks=[i.Check(action=i.ACTION_AGENT_RUN, resource_id=workspace_id)],
        )
        try:
            response = await self._identity.invoke(
                "Authorize", request, token=principal.access_token, timeout_s=self._timeout
            )
        except ServiceError as err:
            if err.code is ErrorCode.UNAUTHENTICATED:
                raise AgentError(AgentErrorKind.UNAUTHENTICATED, "not signed in") from err
            raise
        if not (response.allowed and all(response.allowed)):
            raise AgentError(AgentErrorKind.PERMISSION_DENIED, "not allowed to use the agent in this workspace")
