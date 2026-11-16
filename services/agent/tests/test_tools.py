"""The tool catalogue, schemas derived from protos, the executor and cache."""

from __future__ import annotations

import pytest
from support import NOTES, WORKSPACE

from skein_agent import pb
from skein_agent.app.tools import render
from skein_agent.app.tools.base import RpcTool, ToolRunContext
from skein_agent.app.tools.catalogue import build_catalogue
from skein_agent.app.tools.executor import ToolCall, ToolExecutor
from skein_agent.app.tools.schema import message_schema, proto_comments
from skein_agent.domain.policy import Effect
from skein_agent.domain.untrusted import SourceRegistry
from skein_agent.fakes.infra import FakeClock, InMemoryCache

REQUIRED_TOOLS = {
    "search",
    "suggest_titles",
    "get_note",
    "list_notes",
    "create_note",
    "rename_note",
    "apply_edit",
    "delete_note",
    "set_note_role",
    "create_share_link",
    "blast_radius",
    "shortest_path",
    "reading_plan",
    "semantic_search",
    "discover",
    "list_clusters",
}
_SERVICES = {
    "documents": (pb.documents_pb2, "DocumentService"),
    "search": (pb.search_pb2, "SearchService"),
    "graph": (pb.search_pb2, "GraphService"),
    "ml": (pb.ml_pb2, "MLService"),
    "identity": (pb.identity_pb2, "IdentityService"),
    "notifications": (pb.notifications_pb2, "NotificationService"),
}


def ctx_for(fx, user: str = "alice") -> ToolRunContext:
    token = fx.tokens[user]
    return ToolRunContext(
        "run-1", user, WORKSPACE, token, "n0nce", SourceRegistry(), fx.workspace.services(), deadline=1e18
    )


def test_catalogue_has_every_required_tool_with_unique_names() -> None:
    names = [t.name for t in build_catalogue()]
    assert len(names) == len(set(names))
    assert set(names) >= REQUIRED_TOOLS


def test_every_rpc_tool_names_a_real_rpc_and_its_input_type() -> None:
    for tool in build_catalogue():
        if not isinstance(tool, RpcTool):
            continue
        module, service = _SERVICES[tool.service]
        method = module.DESCRIPTOR.services_by_name[service].methods_by_name[tool.rpc]
        assert method.input_type.full_name == tool.request_type.DESCRIPTOR.full_name, tool.name


def test_schemas_mirror_the_request_descriptor_minus_agent_filled_fields() -> None:
    for tool in build_catalogue():
        if not isinstance(tool, RpcTool):
            continue
        schema = tool.schema()
        excluded = {*tool.from_context, *tool.constants, *tool.hidden}
        expected = [
            f.name for f in tool.request_type.DESCRIPTOR.fields if f.name not in excluded and f.type != f.TYPE_BYTES
        ]
        assert list(schema["properties"]) == expected, tool.name
        assert schema["additionalProperties"] is False
        assert set(schema.get("required", [])) == set(tool.required)
        assert "workspace_id" not in schema["properties"], "the model never picks the workspace"


def test_schema_maps_enums_timestamps_and_nested_messages() -> None:
    share = message_schema(pb.identity_pb2.CreateShareLinkRequest.DESCRIPTOR)["properties"]
    assert share["role"] == {
        "type": "string",
        "enum": ["NOTE_ROLE_UNSPECIFIED", "NOTE_ROLE_VIEWER", "NOTE_ROLE_COMMENTER", "NOTE_ROLE_EDITOR"],
    }
    assert share["expires_at"] == {
        "type": "string",
        "format": "date-time",
        "description": "When the link stops working. Unset means it never expires.",
    }
    search = message_schema(pb.search_pb2.SearchRequest.DESCRIPTOR)["properties"]
    assert search["languages"]["type"] == "array" and search["languages"]["items"] == {"type": "string"}
    assert search["page"]["properties"]["page_size"]["minimum"] == 0
    assert "Only code blocks" in search["languages"]["description"], "descriptions come from proto comments"
    blast = message_schema(pb.search_pb2.BlastRadiusResponse.DESCRIPTOR)["properties"]
    assert blast["hops"] == {
        "type": "object",
        "additionalProperties": {"type": "integer", "minimum": 0},
        "description": blast["hops"]["description"],
    }
    apply_ops = message_schema(pb.documents_pb2.ApplyOpsRequest.DESCRIPTOR)["properties"]
    assert "ops" not in apply_ops, "bytes are never asked of the model"


def test_proto_comments_cover_rpcs() -> None:
    comments = proto_comments()
    assert "trash" in comments["skein.v1.DocumentService.DeleteNote"]
    blast = comments["skein.v1.GraphService.BlastRadius"]
    assert blast.startswith("Get a note's blast radius. The notes within reach"), "summary joined as a sentence"
    assert "BLAST_DIRECTION_CITED_BY" in blast


def test_confirmation_policy_covers_every_destructive_and_outward_tool() -> None:
    ex = ToolExecutor(build_catalogue(), InMemoryCache())
    confirm = {t.name for t in ex.tools if ex.needs_confirmation(t.name)}
    assert confirm == {"delete_note", "set_note_role", "create_share_link"}
    assert all(
        t.effect is Effect.READ for t in ex.tools if t.name in {"search", "get_note", "list_notes", "semantic_search"}
    )


def test_there_is_no_setting_to_skip_confirmation() -> None:
    from dataclasses import fields

    from skein_agent.app.runs import RunSettings
    from skein_agent.config import Settings

    names = {f.name for f in fields(Settings)} | {f.name for f in fields(RunSettings)}
    assert not {n for n in names if "confirm" in n} - {"confirmation_timeout_s"}


async def test_a_new_rpc_becomes_a_tool_with_one_definition(fx) -> None:
    """GetLinkGraph is not in the catalogue; one RpcTool entry is enough."""
    tool = RpcTool(
        "link_graph",
        "graph",
        "GetLinkGraph",
        pb.search_pb2.GetLinkGraphRequest,
        Effect.READ,
        lambda resp, c: render.wrapped_json(resp, c, f"{len(resp.edges)} links"),
        from_context=("workspace_id",),
    )
    assert tool.schema() == {"type": "object", "properties": {}, "additionalProperties": False}
    ex = ToolExecutor([tool], InMemoryCache())
    out = await ex.execute(ToolCall("c", "link_graph", {}), ctx_for(fx))
    assert not out.is_error and out.summary == "5 links"
    assert NOTES["fugue"] in out.content


async def test_read_results_are_cached_per_user_and_invalidated_by_writes(fx) -> None:
    clock = FakeClock()
    cache = InMemoryCache(clock)
    ex = ToolExecutor(build_catalogue(), cache, cache_ttl_s=30)
    alice, bob = ctx_for(fx, "alice"), ctx_for(fx, "bob")
    search = ToolCall("c", "list_notes", {})

    first = await ex.execute(search, alice)
    second = await ex.execute(search, alice)
    assert (first.cached, second.cached) == (False, True)
    assert first.content == second.content
    assert not (await ex.execute(search, bob)).cached, "never shared across users"

    await ex.execute(ToolCall("w", "create_note", {"title": "New"}), alice)
    after_write = await ex.execute(search, alice)
    assert not after_write.cached and "New" in after_write.content

    clock.advance(31)
    assert not (await ex.execute(search, alice)).cached, "entries expire"


async def test_cached_results_still_get_fresh_source_labels_per_run(fx) -> None:
    ex = ToolExecutor(build_catalogue(), InMemoryCache())
    call = ToolCall("c", "search", {"query": "Fugue interleaving"})
    run_a, run_b = ctx_for(fx), ctx_for(fx)
    run_b.sources.add("some-other-note")
    await ex.execute(call, run_a)
    out = await ex.execute(call, run_b)
    assert out.cached and 'source="S2"' in out.content and len(run_b.sources) == 2


async def test_failures_are_not_cached(fx) -> None:
    cache = InMemoryCache()
    ex = ToolExecutor(build_catalogue(), cache)
    await ex.execute(ToolCall("c", "get_note", {"note_id": "missing"}), ctx_for(fx))
    assert not [k for k in cache.keys() if ":tool:" in k]  # noqa: SIM118 (keys() drops expired entries)


async def test_share_link_token_reaches_the_model_but_not_the_trace(fx) -> None:
    ex = ToolExecutor(build_catalogue(), InMemoryCache())
    out = await ex.execute(
        ToolCall("c", "create_share_link", {"note_id": NOTES["fugue"], "role": "NOTE_ROLE_VIEWER"}), ctx_for(fx)
    )
    assert "share-secret-" in out.content
    assert "share-secret-" not in out.trace_content and "[redacted]" in out.trace_content


@pytest.mark.parametrize(
    "tool,args,summary",
    [
        ("blast_radius", {"note_id": NOTES["crdt"]}, "Reached 3 notes within 2 hops"),
        ("shortest_path", {"from": NOTES["fugue"], "to": NOTES["raft_log"]}, "Found a path of 4 notes"),
        ("reading_plan", {"topic": "rust", "minutes": 15}, "Planned 1 note (7 min)"),
        ("semantic_search", {"query": "leader election timeouts"}, "Found 2 semantic matches"),
        ("discover", {"note_id": NOTES["raft"], "k": 2}, "Found 2 related notes"),
        ("list_clusters", {}, "Listed 5 clusters"),
        ("suggest_titles", {"prefix": "Raft"}, "Found 3 titles"),
        ("list_inbox", {}, "Read 0 notifications"),
    ],
)
async def test_each_read_tool_reaches_its_rpc_and_wraps_its_result(fx, tool, args, summary) -> None:
    ex = ToolExecutor(build_catalogue(), InMemoryCache())
    out = await ex.execute(ToolCall("c", tool, args), ctx_for(fx))
    assert not out.is_error, out.content
    assert out.summary == summary
    assert out.content.startswith('<untrusted_data nonce="n0nce"'), "read results are always wrapped"
