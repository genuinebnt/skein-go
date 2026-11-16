"""JSON Schema for tool arguments, derived from proto message descriptors.

ADR-0009 requires tool schemas to come from the proto files, so the agent
cannot drift from the APIs it calls. ``schema_for`` walks a request
message's descriptor and maps each field:

| proto | JSON Schema |
|---|---|
| string | string |
| bool | boolean |
| int32/sint32/sfixed32, int64... | integer |
| uint32/fixed32, uint64/fixed64 | integer, minimum 0 |
| float, double | number |
| enum | string, one of the value names |
| message | object, recursively |
| google.protobuf.Timestamp | string, format date-time (RFC 3339) |
| repeated T | array of T |
| map<K, V> | object with additionalProperties V |
| bytes | omitted: no tool takes raw bytes from the model |

Field names stay snake_case; ``google.protobuf.json_format.ParseDict``
accepts them, and it also rejects wrong types and unknown enum names,
which is the validation the executor relies on.

Descriptions come from the proto comments when the descriptor set with
source info (``gen/skein.binpb``) is available; without it schemas are
still complete, only less descriptive.
"""

from __future__ import annotations

from collections.abc import Iterable
from functools import cache
from pathlib import Path
from typing import Any

from google.protobuf import descriptor_pb2
from google.protobuf.descriptor import Descriptor, FieldDescriptor

from ... import pb

_SCALARS: dict[int, dict[str, Any]] = {
    FieldDescriptor.TYPE_STRING: {"type": "string"},
    FieldDescriptor.TYPE_BOOL: {"type": "boolean"},
    FieldDescriptor.TYPE_INT32: {"type": "integer"},
    FieldDescriptor.TYPE_SINT32: {"type": "integer"},
    FieldDescriptor.TYPE_SFIXED32: {"type": "integer"},
    FieldDescriptor.TYPE_INT64: {"type": "integer"},
    FieldDescriptor.TYPE_SINT64: {"type": "integer"},
    FieldDescriptor.TYPE_SFIXED64: {"type": "integer"},
    FieldDescriptor.TYPE_UINT32: {"type": "integer", "minimum": 0},
    FieldDescriptor.TYPE_FIXED32: {"type": "integer", "minimum": 0},
    FieldDescriptor.TYPE_UINT64: {"type": "integer", "minimum": 0},
    FieldDescriptor.TYPE_FIXED64: {"type": "integer", "minimum": 0},
    FieldDescriptor.TYPE_FLOAT: {"type": "number"},
    FieldDescriptor.TYPE_DOUBLE: {"type": "number"},
}

_MAX_DEPTH = 4  # proto messages here nest at most two deep; this stops cycles


@cache
def proto_comments(path: Path = pb.DESCRIPTOR_SET_PATH) -> dict[str, str]:
    """Map fully qualified names (``skein.v1.GetNoteRequest.note_id``,
    ``skein.v1.DocumentService.GetNote``) to their leading or trailing
    proto comment. Empty if the descriptor set is missing."""
    if not path.is_file():
        return {}
    fds = descriptor_pb2.FileDescriptorSet.FromString(path.read_bytes())
    out: dict[str, str] = {}
    for f in fds.file:
        if not f.package.startswith("skein."):
            continue
        by_path = {tuple(loc.path): loc for loc in f.source_code_info.location}

        def comment(loc_path: tuple[int, ...], by_path: dict = by_path) -> str:
            loc = by_path.get(loc_path)
            if loc is None:
                return ""
            text = loc.leading_comments or loc.trailing_comments
            return " ".join(text.split())

        def visit_message(m: descriptor_pb2.DescriptorProto, prefix: str, loc: tuple[int, ...]) -> None:
            name = f"{prefix}.{m.name}"
            if c := comment(loc):
                out[name] = c
            for i, fld in enumerate(m.field):
                if c := comment((*loc, 2, i)):
                    out[f"{name}.{fld.name}"] = c
            for i, nested in enumerate(m.nested_type):
                visit_message(nested, name, (*loc, 3, i))

        for i, m in enumerate(f.message_type):
            visit_message(m, f.package, (4, i))
        for i, svc in enumerate(f.service):
            for j, method in enumerate(svc.method):
                if c := comment((6, i, 2, j)):
                    out[f"{f.package}.{svc.name}.{method.name}"] = c
    return out


def _field_schema(field: FieldDescriptor, comments: dict[str, str], depth: int) -> dict[str, Any] | None:
    if field.type == FieldDescriptor.TYPE_BYTES:
        return None
    if field.message_type is not None and field.message_type.GetOptions().map_entry:
        value_field = field.message_type.fields_by_name["value"]
        value = _single_schema(value_field, comments, depth)
        if value is None:
            return None
        return {"type": "object", "additionalProperties": value}
    single = _single_schema(field, comments, depth)
    if single is None:
        return None
    if field.is_repeated:
        return {"type": "array", "items": single}
    return single


def _single_schema(field: FieldDescriptor, comments: dict[str, str], depth: int) -> dict[str, Any] | None:
    if field.type == FieldDescriptor.TYPE_BYTES:
        return None
    if field.type == FieldDescriptor.TYPE_ENUM:
        return {"type": "string", "enum": [v.name for v in field.enum_type.values]}
    if field.type == FieldDescriptor.TYPE_MESSAGE:
        if field.message_type.full_name == "google.protobuf.Timestamp":
            return {"type": "string", "format": "date-time"}
        if depth >= _MAX_DEPTH:
            return None
        return message_schema(field.message_type, comments=comments, depth=depth + 1)
    return dict(_SCALARS[field.type])


def message_schema(
    descriptor: Descriptor,
    *,
    exclude: Iterable[str] = (),
    required: Iterable[str] = (),
    comments: dict[str, str] | None = None,
    depth: int = 0,
) -> dict[str, Any]:
    """JSON Schema for a message. ``exclude`` drops fields the agent fills in
    itself (``workspace_id``) or that the model should not set."""
    comments = proto_comments() if comments is None else comments
    excluded = set(exclude)
    properties: dict[str, Any] = {}
    for field in descriptor.fields:  # declaration order, stable
        if field.name in excluded:
            continue
        schema = _field_schema(field, comments, depth)
        if schema is None:
            continue
        if desc := comments.get(f"{descriptor.full_name}.{field.name}"):
            schema = {**schema, "description": desc}
        properties[field.name] = schema
    req = [r for r in required if r in properties]
    missing = set(required) - set(properties)
    if missing:
        raise ValueError(f"{descriptor.full_name}: required fields {sorted(missing)} are not in the schema")
    out: dict[str, Any] = {"type": "object", "properties": properties, "additionalProperties": False}
    if req:
        out["required"] = req
    return out
