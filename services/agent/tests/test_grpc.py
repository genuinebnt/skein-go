"""The gRPC edge and the gRPC client adapters, over an in-process server on
the loopback interface (no external network, no other service)."""

from __future__ import annotations

import asyncio
import json
from collections.abc import AsyncIterator

import grpc
import pytest
from support import NOTES, WORKSPACE

from skein_agent import pb
from skein_agent.adapters.grpc_clients import GrpcOpLogReader, GrpcServiceClient
from skein_agent.adapters.grpc_server import AgentServicer
from skein_agent.domain import crdt
from skein_agent.domain.errors import ErrorCode, ServiceError
from skein_agent.domain.ratelimit import RateLimit
from skein_agent.fakes.infra import FakeClock, InMemoryRateLimiter
from skein_agent.fakes.model import call, say

a = pb.agent_pb2


async def _serve(servicer: object, adder) -> tuple[grpc.aio.Server, grpc.aio.Channel]:
    server = grpc.aio.server()
    adder(servicer, server)
    port = server.add_insecure_port("127.0.0.1:0")
    await server.start()
    return server, grpc.aio.insecure_channel(f"127.0.0.1:{port}")


@pytest.fixture
async def agent(make_harness, request):
    script = request.param if hasattr(request, "param") else [say("hello")]
    h = make_harness(script)
    server, channel = await _serve(AgentServicer(h.runs, h.chats), pb.agent_pb2_grpc.add_AgentServiceServicer_to_server)
    yield h, pb.agent_pb2_grpc.AgentServiceStub(channel)
    await channel.close()
    await server.stop(0)


def md(h, user="alice"):
    return (("x-skein-user-id", user), ("authorization", f"Bearer {h.tokens[user]}"), ("x-request-id", "req-1"))


@pytest.mark.parametrize(
    "agent",
    [
        [
            call("search", {"query": "Fugue interleaving"}),
            call("delete_note", {"note_id": NOTES["scratch"]}),
            say("Fugue avoids interleaving [S1]. Scratch deleted."),
        ]
    ],
    indirect=True,
)
async def test_send_message_streams_every_event_kind_and_confirm_resumes(agent):
    h, stub = agent
    chat = await stub.CreateChat(a.CreateChatRequest(workspace_id=WORKSPACE), metadata=md(h))
    stream = stub.SendMessage(
        a.SendMessageRequest(chat_id=chat.chat_id, text="fugue, and delete scratch"), metadata=md(h)
    )
    kinds, run_id = [], ""
    async for msg in stream:
        kind = msg.WhichOneof("event")
        kinds.append(kind)
        run_id = msg.run_id
        if kind == "confirmation":
            assert msg.confirmation.tool == "delete_note"
            assert json.loads(msg.confirmation.arguments_json) == {"note_id": NOTES["scratch"]}
            await stub.Confirm(
                a.ConfirmRequest(run_id=run_id, confirmation_id=msg.confirmation.confirmation_id, approved=True),
                metadata=md(h),
            )
        if kind == "citation":
            assert msg.citation.location.note_id == NOTES["fugue"] and msg.citation.location.block_id
        if kind == "finished":
            assert msg.finished.outcome == a.RunFinished.OUTCOME_COMPLETED and msg.finished.steps == 3
    assert {"tool_call", "tool_result", "confirmation", "text_delta", "citation", "finished"} == set(kinds)
    assert kinds[-1] == "finished"
    got = await stub.GetRun(a.GetRunRequest(run_id=run_id), metadata=md(h))
    assert [e.WhichOneof("event") for e in got.events] == kinds
    listed = await stub.ListChats(a.ListChatsRequest(workspace_id=WORKSPACE), metadata=md(h))
    assert [c.chat_id for c in listed.chats] == [chat.chat_id] and listed.chats[0].title


async def test_missing_credentials_are_unauthenticated(agent):
    _, stub = agent
    with pytest.raises(grpc.aio.AioRpcError) as err:
        await stub.CreateChat(a.CreateChatRequest(workspace_id=WORKSPACE), metadata=(("x-skein-user-id", "alice"),))
    assert err.value.code() == grpc.StatusCode.UNAUTHENTICATED


async def test_a_forged_user_header_without_that_users_token_gets_nothing(agent):
    h, stub = agent
    forged = (("x-skein-user-id", "alice"), ("authorization", f"Bearer {h.tokens['bob']}"))
    with pytest.raises(grpc.aio.AioRpcError) as err:
        await stub.CreateChat(a.CreateChatRequest(workspace_id=WORKSPACE), metadata=forged)
    assert err.value.code() == grpc.StatusCode.PERMISSION_DENIED


async def test_other_users_runs_are_not_found(agent):
    h, stub = agent
    chat = await stub.CreateChat(a.CreateChatRequest(workspace_id=WORKSPACE), metadata=md(h))
    run_id = ""
    async for msg in stub.SendMessage(a.SendMessageRequest(chat_id=chat.chat_id, text="hi"), metadata=md(h)):
        run_id = msg.run_id
    with pytest.raises(grpc.aio.AioRpcError) as err:
        await stub.GetRun(a.GetRunRequest(run_id=run_id), metadata=md(h, "carol"))
    assert err.value.code() == grpc.StatusCode.NOT_FOUND


async def test_rate_limited_runs_get_resource_exhausted_with_retry_after(make_harness):
    limiter = InMemoryRateLimiter(RateLimit(1, 60, 0), FakeClock())
    h = make_harness([say("one"), say("two")], rate_limiter=limiter)
    server, channel = await _serve(AgentServicer(h.runs, h.chats), pb.agent_pb2_grpc.add_AgentServiceServicer_to_server)
    stub = pb.agent_pb2_grpc.AgentServiceStub(channel)
    try:
        chat = await stub.CreateChat(a.CreateChatRequest(workspace_id=WORKSPACE), metadata=md(h))
        async for _ in stub.SendMessage(a.SendMessageRequest(chat_id=chat.chat_id, text="hi"), metadata=md(h)):
            pass
        call_ = stub.SendMessage(a.SendMessageRequest(chat_id=chat.chat_id, text="again"), metadata=md(h))
        with pytest.raises(grpc.aio.AioRpcError) as err:
            async for _ in call_:
                pass
        assert err.value.code() == grpc.StatusCode.RESOURCE_EXHAUSTED
        assert dict(err.value.trailing_metadata())["retry-after"] == "60"
    finally:
        await channel.close()
        await server.stop(0)


async def _wait_finished(h, run_id: str):
    for _ in range(200):
        trace = await h.run_store.get(run_id)
        if trace.events() and trace.events()[-1].kind == "finished":
            return trace.events()[-1]
        await asyncio.sleep(0.02)
    raise AssertionError("the run never finished")


@pytest.mark.parametrize("agent", [[call("delete_note", {"note_id": NOTES["scratch"]}), say("done")]], indirect=True)
async def test_client_disconnect_leaves_a_paused_run_answerable_by_confirm_and_get_run(agent):
    h, stub = agent
    chat = await stub.CreateChat(a.CreateChatRequest(workspace_id=WORKSPACE), metadata=md(h))
    stream = stub.SendMessage(a.SendMessageRequest(chat_id=chat.chat_id, text="delete scratch"), metadata=md(h))
    async for msg in stream:
        if msg.WhichOneof("event") == "confirmation":
            run_id, confirmation_id = msg.run_id, msg.confirmation.confirmation_id
            break
    stream.cancel()
    await asyncio.sleep(0.1)
    assert (await h.run_store.get(run_id)).status.value == "paused", "a disconnect does not cancel a pause"
    request = a.ConfirmRequest(run_id=run_id, confirmation_id=confirmation_id, approved=True)
    await stub.Confirm(request, metadata=md(h))
    assert (await _wait_finished(h, run_id)).outcome.name == "COMPLETED"
    assert h.workspace.notes[NOTES["scratch"]].trashed
    call_ = stub.GetRun(a.GetRunRequest(run_id=run_id), metadata=md(h))
    got = await call_
    usage = json.loads(dict(await call_.initial_metadata())["x-skein-run-usage"])
    finished = got.events[-1].finished
    assert usage["status"] == "finished" and usage["steps"] == finished.steps == 2
    assert usage["input_tokens"] == finished.input_tokens and usage["output_tokens"] == finished.output_tokens
    assert usage["cost_usd"] > 0 and [c["step"] for c in usage["model_calls"]] == [1, 2]
    assert usage["cost_usd"] == pytest.approx(sum(c["cost_usd"] for c in usage["model_calls"]))


@pytest.mark.parametrize(
    "agent", [[call("search", {"query": "Fugue interleaving"}), say("Fugue avoids interleaving [S1].")]], indirect=True
)
async def test_client_disconnect_mid_tool_call_leaves_the_run_going_to_completion(agent, fx):
    h, stub = agent
    entered, release = asyncio.Event(), asyncio.Event()

    async def slow(request):
        entered.set()
        await release.wait()

    fx.workspace.hooks[("search", "Search")] = slow
    chat = await stub.CreateChat(a.CreateChatRequest(workspace_id=WORKSPACE), metadata=md(h))
    stream = stub.SendMessage(a.SendMessageRequest(chat_id=chat.chat_id, text="what does fugue avoid"), metadata=md(h))
    run_id = (await stream.read()).run_id
    await asyncio.wait_for(entered.wait(), 5)
    stream.cancel()
    await asyncio.sleep(0.1)
    release.set()  # the tool call finishes after the client has gone
    assert (await _wait_finished(h, run_id)).outcome.name == "COMPLETED", "only CancelRun cancels"
    got = await stub.GetRun(a.GetRunRequest(run_id=run_id), metadata=md(h))
    kinds = [e.WhichOneof("event") for e in got.events]
    assert {"tool_call", "tool_result", "text_delta", "citation"} <= set(kinds) and kinds[-1] == "finished"


class _Docs(pb.documents_pb2_grpc.DocumentServiceServicer):
    """A tiny DocumentService for testing the client adapters."""

    def __init__(self, seq: crdt.Sequence) -> None:
        self.seq = seq
        self.seen_metadata: list[dict[str, str]] = []

    async def GetNote(self, request, context):
        self.seen_metadata.append(dict(context.invocation_metadata()))
        await context.abort(grpc.StatusCode.PERMISSION_DENIED, "viewer cannot do that")

    async def Sync(self, request_iterator, context) -> AsyncIterator:
        hello = await request_iterator.__anext__()
        assert hello.hello.replica_id == 77
        d = pb.documents_pb2
        ops = self.seq.ops_since({})
        yield d.SyncResponse(presence=d.Presence(user_id="someone"))
        for k in range(0, len(ops), 4):  # several batches
            yield d.SyncResponse(ops=crdt.encode_ops(ops[k : k + 4]))
        await asyncio.sleep(30)  # a live stream never ends by itself


async def test_grpc_client_sends_the_bearer_token_and_maps_status_codes():
    seq = crdt.Sequence(5)
    seq.insert(0, "hello")
    docs = _Docs(seq)
    server, channel = await _serve(docs, pb.documents_pb2_grpc.add_DocumentServiceServicer_to_server)
    try:
        client = GrpcServiceClient(channel, pb.documents_pb2_grpc.DocumentServiceStub)
        with pytest.raises(ServiceError) as err:
            await client.invoke("GetNote", pb.documents_pb2.GetNoteRequest(note_id="n"), token="tok-123", timeout_s=5)
        assert err.value.code is ErrorCode.PERMISSION_DENIED and err.value.message == "viewer cannot do that"
        assert "tok-123" not in str(err.value)
        assert docs.seen_metadata[0]["authorization"] == "Bearer tok-123"
    finally:
        await channel.close()
        await server.stop(0)


async def test_op_log_reader_collects_batches_until_the_version_is_covered():
    seq = crdt.Sequence(5)
    seq.insert(0, "hello world")
    seq.delete(0, 6)
    server, channel = await _serve(_Docs(seq), pb.documents_pb2_grpc.add_DocumentServiceServicer_to_server)
    try:
        reader = GrpcOpLogReader(channel)
        batches = await asyncio.wait_for(reader.fetch_ops("n", seq.version(), replica_id=77, token="t", timeout_s=5), 5)
        replay = crdt.Sequence(77)
        for b in batches:
            for op in crdt.decode_ops(b):
                replay.apply(op)
        assert replay.text() == "world" and len(batches) == 5
    finally:
        await channel.close()
        await server.stop(0)


async def test_admin_endpoints_report_health_and_readiness_until_draining():
    from skein_agent.adapters.admin import start_admin

    draining = asyncio.Event()
    server = await start_admin("127.0.0.1:0", lambda: not draining.is_set())
    port = server.sockets[0].getsockname()[1]

    async def get(path: str, method: str = "GET") -> int:
        reader, writer = await asyncio.open_connection("127.0.0.1", port)
        writer.write(f"{method} {path} HTTP/1.1\r\nHost: x\r\n\r\n".encode())
        await writer.drain()
        status = int((await reader.readline()).split()[1])
        writer.close()
        return status

    try:
        assert [await get("/healthz"), await get("/readyz"), await get("/metrics")] == [200, 200, 404]
        assert await get("/readyz", "POST") == 405
        draining.set()
        assert [await get("/healthz"), await get("/readyz")] == [200, 503], "draining: alive, not ready"
    finally:
        server.close()
        await server.wait_closed()
