"""Smoke tests for the generated package: it installs, imports and speaks
the same JSON as the gateway.

These guard the import rewriting in scripts/gen-python.sh (a missed rewrite
fails at import time) and the lowerCamelCase JSON names the REST docs use.
"""

import importlib
import pkgutil

import grpc
from google.protobuf import json_format

import skein_proto.v1
from skein_proto.v1 import documents_pb2, documents_pb2_grpc, search_pb2

SERVICES = {
    "agent_pb2_grpc": ["AgentServiceStub", "AgentServiceServicer"],
    "documents_pb2_grpc": ["DocumentServiceStub", "DocumentServiceServicer"],
    "identity_pb2_grpc": ["IdentityServiceStub", "IdentityServiceServicer"],
    "ml_pb2_grpc": ["MLServiceStub", "MLServiceServicer"],
    "notifications_pb2_grpc": ["NotificationServiceStub", "NotificationServiceServicer"],
    "search_pb2_grpc": ["SearchServiceStub", "GraphServiceStub", "GraphServiceServicer"],
}


PROTO_FILES = [
    "agent", "api", "common", "documents", "events",
    "identity", "ml", "notifications", "search",
]


def test_every_proto_file_has_messages_and_grpc_modules_that_import():
    names = sorted(m.name for m in pkgutil.iter_modules(skein_proto.v1.__path__))
    expected = sorted([f"{f}_pb2" for f in PROTO_FILES] + [f"{f}_pb2_grpc" for f in PROTO_FILES])
    assert names == expected
    for name in names:
        importlib.import_module(f"skein_proto.v1.{name}")


def test_every_service_has_a_stub_and_servicer():
    for module, classes in SERVICES.items():
        mod = importlib.import_module(f"skein_proto.v1.{module}")
        for cls in classes:
            assert hasattr(mod, cls), f"{module}.{cls}"


def test_descriptor_pool_uses_canonical_proto_file_names():
    assert documents_pb2.DESCRIPTOR.name == "skein/v1/documents.proto"
    deps = {d.name for d in documents_pb2.DESCRIPTOR.dependencies}
    assert "skein/v1/common.proto" in deps
    assert "protoc-gen-openapiv2/options/annotations.proto" in deps


def test_json_uses_lower_camel_case_and_string_uint64():
    request = documents_pb2.ApplyOpsRequest(
        note_id="01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f",
        replica_id=4831727201934127107,
        ops=b"\x01\x02",
    )
    as_json = json_format.MessageToDict(request)
    assert as_json == {
        "noteId": "01936f40-2b3c-7d4e-9f50-6a7b8c9d0e1f",
        "replicaId": "4831727201934127107",
        "ops": "AQI=",
    }
    assert json_format.ParseDict(as_json, documents_pb2.ApplyOpsRequest()) == request


def test_blast_direction_defaults_to_unspecified():
    request = search_pb2.BlastRadiusRequest(note_id="n")
    assert request.direction == search_pb2.BLAST_DIRECTION_UNSPECIFIED
    assert search_pb2.BlastDirection.Name(1) == "BLAST_DIRECTION_CITED_BY"


def test_stub_round_trips_through_an_in_process_server():
    class Documents(documents_pb2_grpc.DocumentServiceServicer):
        def GetNote(self, request, context):
            return documents_pb2.GetNoteResponse(
                note=documents_pb2.Note(id=request.note_id, title="t")
            )

    from concurrent import futures

    server = grpc.server(futures.ThreadPoolExecutor(max_workers=1))
    documents_pb2_grpc.add_DocumentServiceServicer_to_server(Documents(), server)
    port = server.add_insecure_port("127.0.0.1:0")
    server.start()
    try:
        with grpc.insecure_channel(f"127.0.0.1:{port}") as channel:
            stub = documents_pb2_grpc.DocumentServiceStub(channel)
            reply = stub.GetNote(documents_pb2.GetNoteRequest(note_id="abc"), timeout=5)
        assert reply.note.id == "abc"
    finally:
        server.stop(grace=None)
