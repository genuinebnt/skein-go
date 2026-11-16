"""Every tool the agent has, one entry each.

To expose a new RPC: add an ``RpcTool`` with its service, RPC name and
request type, an effect, and a renderer. The argument schema comes from the
request descriptor and the description from the RPC's proto comment unless
one is given. Effects decide confirmation and caching (``domain/policy.py``);
there is no per-tool switch.

Descriptions here are written for the model: what the tool is for and when
to use it, which the proto comments (written for API users) do not always
say.
"""

from __future__ import annotations

from ... import pb
from ...domain.policy import Effect
from . import render as r
from .base import Authorization, RpcTool, Tool
from .edit import ApplyEditTool

_d = pb.documents_pb2
_s = pb.search_pb2
_m = pb.ml_pb2
_i = pb.identity_pb2
_n = pb.notifications_pb2


def build_catalogue() -> list[Tool]:
    """The tools, in the order the model sees them."""
    ws = ("workspace_id",)
    return [
        RpcTool(
            "search",
            "search",
            "Search",
            _s.SearchRequest,
            Effect.READ,
            r.render_search,
            description="Full-text or regex search over notes the user can read. Returns snippets "
            "with citable sources. Use for facts and quotes.",
            from_context=ws,
            required=("query",),
        ),
        RpcTool(
            "suggest_titles",
            "search",
            "SuggestTitles",
            _s.SuggestTitlesRequest,
            Effect.READ,
            r.render_suggest_titles,
            description="Find notes by (possibly misspelled) title prefix; returns note IDs.",
            from_context=ws,
            required=("prefix",),
        ),
        RpcTool(
            "get_note",
            "documents",
            "GetNote",
            _d.GetNoteRequest,
            Effect.READ,
            r.render_get_note,
            description="Read a note's full text by ID. The text is a citable source.",
            constants={"include_markdown": True},
            required=("note_id",),
        ),
        RpcTool(
            "list_notes",
            "documents",
            "ListNotes",
            _d.ListNotesRequest,
            Effect.READ,
            r.render_list_notes,
            description="List notes in the workspace (titles and IDs).",
            from_context=ws,
        ),
        RpcTool(
            "create_note",
            "documents",
            "CreateNote",
            _d.CreateNoteRequest,
            Effect.WRITE,
            r.render_create_note,
            description="Create a note with a title and optional Markdown.",
            from_context=ws,
            required=("title",),
        ),
        RpcTool(
            "rename_note",
            "documents",
            "RenameNote",
            _d.RenameNoteRequest,
            Effect.WRITE,
            r.render_rename_note,
            required=("note_id", "title"),
        ),
        ApplyEditTool(),
        RpcTool(
            "delete_note",
            "documents",
            "DeleteNote",
            _d.DeleteNoteRequest,
            Effect.DESTRUCTIVE,
            r.render_delete_note,
            description="Move a note to the trash. Only when the user explicitly asks; the user is asked to confirm.",
            required=("note_id",),
            describe_fn=r.describe_delete_note,
            authorization=Authorization(_i.ACTION_NOTE_DELETE, "note_id"),
        ),
        RpcTool(
            "set_note_role",
            "identity",
            "SetNoteRole",
            _i.SetNoteRoleRequest,
            Effect.OUTWARD,
            r.render_set_note_role,
            description="Change someone's role on a note (share or unshare). Only when the user "
            "explicitly asks; the user is asked to confirm.",
            required=("note_id", "user_id"),
            describe_fn=r.describe_set_note_role,
            authorization=Authorization(_i.ACTION_NOTE_SHARE, "note_id"),
        ),
        RpcTool(
            "create_share_link",
            "identity",
            "CreateShareLink",
            _i.CreateShareLinkRequest,
            Effect.OUTWARD,
            r.render_create_share_link,
            description="Create a link that grants a role on a note to anyone holding it. Only "
            "when the user explicitly asks; the user is asked to confirm.",
            required=("note_id", "role"),
            describe_fn=r.describe_create_share_link,
            authorization=Authorization(_i.ACTION_NOTE_SHARE, "note_id"),
        ),
        RpcTool(
            "blast_radius",
            "graph",
            "BlastRadius",
            _s.BlastRadiusRequest,
            Effect.READ,
            r.render_blast_radius,
            required=("note_id",),
        ),
        RpcTool(
            "shortest_path",
            "graph",
            "ShortestPath",
            _s.ShortestPathRequest,
            Effect.READ,
            r.render_shortest_path,
            description="Shortest chain of links between two notes (by note ID).",
            required=("from", "to"),
        ),
        RpcTool(
            "reading_plan",
            "graph",
            "ReadingPlan",
            _s.ReadingPlanRequest,
            Effect.READ,
            r.render_reading_plan,
            from_context=ws,
            required=("topic", "minutes"),
        ),
        RpcTool(
            "semantic_search",
            "ml",
            "SemanticSearch",
            _m.SemanticSearchRequest,
            Effect.READ,
            r.render_semantic_search,
            description="Find passages by meaning rather than words. Returns citable locations "
            "without text; read the notes for the words.",
            from_context=ws,
            hidden=("index", "measure_recall"),
            required=("query",),
        ),
        RpcTool(
            "discover", "ml", "Discover", _m.DiscoverRequest, Effect.READ, r.render_discover, required=("note_id",)
        ),
        RpcTool(
            "list_clusters",
            "ml",
            "ListClusters",
            _m.ListClustersRequest,
            Effect.READ,
            r.render_list_clusters,
            description="Topic clusters of the workspace's notes.",
            from_context=ws,
        ),
        RpcTool(
            "list_inbox",
            "notifications",
            "ListInbox",
            _n.ListInboxRequest,
            Effect.READ,
            r.render_list_inbox,
            description="The user's notifications (mentions, comments, shares).",
        ),
    ]
