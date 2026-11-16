"""Real ServiceClient and OpLogReader adapters over grpc.aio.

``GrpcServiceClient`` calls any unary RPC of a generated stub by name, so
the tool catalogue never needs per-RPC client code. Every call carries:

- ``authorization: Bearer <delegated token>``: services check it like any
  user token (ADR-0009);
- ``x-request-id`` from the incoming request, and W3C ``traceparent``
  from the current span, so a run's calls join the request's trace;
- a deadline: the tool's timeout, already capped by the run's remaining
  wall-time budget.

Non-OK statuses become ServiceError with the status details as message;
metadata (which may echo the token) is never included.

``GrpcOpLogReader`` reads a note's operations through ``Sync``: it sends a
hello with an empty version vector and collects op batches until its own
version covers the one GetNote returned, then closes the stream. Presence
messages are ignored.
"""

from __future__ import annotations

import asyncio
from typing import Any

import grpc
from google.protobuf.message import Message
from opentelemetry.propagate import inject

from .. import pb
from ..domain import crdt
from ..domain.errors import ErrorCode, ServiceError
from ..observability import request_id_var

_CODES = {
    grpc.StatusCode.PERMISSION_DENIED: ErrorCode.PERMISSION_DENIED,
    grpc.StatusCode.UNAUTHENTICATED: ErrorCode.UNAUTHENTICATED,
    grpc.StatusCode.NOT_FOUND: ErrorCode.NOT_FOUND,
    grpc.StatusCode.INVALID_ARGUMENT: ErrorCode.INVALID_ARGUMENT,
    grpc.StatusCode.OUT_OF_RANGE: ErrorCode.INVALID_ARGUMENT,
    grpc.StatusCode.FAILED_PRECONDITION: ErrorCode.FAILED_PRECONDITION,
    grpc.StatusCode.ALREADY_EXISTS: ErrorCode.FAILED_PRECONDITION,
    grpc.StatusCode.ABORTED: ErrorCode.FAILED_PRECONDITION,
    grpc.StatusCode.RESOURCE_EXHAUSTED: ErrorCode.RESOURCE_EXHAUSTED,
    grpc.StatusCode.UNAVAILABLE: ErrorCode.UNAVAILABLE,
    grpc.StatusCode.DEADLINE_EXCEEDED: ErrorCode.DEADLINE_EXCEEDED,
    grpc.StatusCode.UNIMPLEMENTED: ErrorCode.UNIMPLEMENTED,
}


def to_service_error(err: grpc.aio.AioRpcError) -> ServiceError:
    """Map a gRPC status to the transport-neutral error."""
    return ServiceError(_CODES.get(err.code(), ErrorCode.INTERNAL), err.details() or err.code().name.lower())


def call_metadata(token: str) -> list[tuple[str, str]]:
    """Credentials and correlation headers for one outgoing call."""
    carrier: dict[str, str] = {}
    inject(carrier)
    md = [("authorization", f"Bearer {token}"), *carrier.items()]
    if rid := request_id_var.get():
        md.append(("x-request-id", rid))
    return md


class GrpcServiceClient:
    """A ServiceClient over one generated stub."""

    def __init__(self, channel: grpc.aio.Channel, stub_type: type[Any]) -> None:
        self._stub = stub_type(channel)

    async def invoke(self, rpc: str, request: Message, *, token: str, timeout_s: float) -> Message:
        """See ServiceClient.invoke."""
        method = getattr(self._stub, rpc, None)
        if method is None:
            raise ServiceError(ErrorCode.UNIMPLEMENTED, f"no RPC {rpc}")
        try:
            return await method(request, metadata=call_metadata(token), timeout=timeout_s)
        except grpc.aio.AioRpcError as err:
            raise to_service_error(err) from None


class GrpcOpLogReader:
    """OpLogReader over DocumentService.Sync."""

    def __init__(self, channel: grpc.aio.Channel) -> None:
        self._stub = pb.documents_pb2_grpc.DocumentServiceStub(channel)

    async def fetch_ops(
        self, note_id: str, until: dict[int, int], *, replica_id: int, token: str, timeout_s: float
    ) -> list[bytes]:
        """See OpLogReader.fetch_ops."""
        d = pb.documents_pb2
        # The run's registered replica; this stream only reads, so the server
        # never receives an operation on it.
        hello = d.SyncRequest(hello=d.SyncHello(note_id=note_id, replica_id=replica_id, version=d.VersionVector()))
        done = asyncio.Event()

        async def requests() -> Any:
            yield hello
            await done.wait()  # keep the request side open until we have enough

        call = self._stub.Sync(requests(), metadata=call_metadata(token), timeout=timeout_s)
        batches: list[bytes] = []
        seen: dict[int, int] = {}
        try:
            if not until:
                return batches
            async for msg in call:
                if msg.WhichOneof("message") != "ops":
                    continue
                batches.append(msg.ops)
                for op in crdt.decode_ops(msg.ops):
                    seen[op.id.replica] = max(seen.get(op.id.replica, 0), op.id.counter)
                if all(seen.get(r, 0) >= c for r, c in until.items()):
                    return batches
            raise ServiceError(ErrorCode.UNAVAILABLE, "sync stream ended before the note was complete")
        except grpc.aio.AioRpcError as err:
            raise to_service_error(err) from None
        except crdt.CrdtError as err:
            raise ServiceError(ErrorCode.INTERNAL, f"undecodable ops from documents ({err.kind.name})") from err
        finally:
            done.set()
            call.cancel()


def build_services(channels: dict[str, grpc.aio.Channel]) -> Any:
    """Services from one channel per downstream service."""
    from ..app.ports import Services

    return Services(
        documents=GrpcServiceClient(channels["documents"], pb.documents_pb2_grpc.DocumentServiceStub),
        search=GrpcServiceClient(channels["search"], pb.search_pb2_grpc.SearchServiceStub),
        graph=GrpcServiceClient(channels["search"], pb.search_pb2_grpc.GraphServiceStub),
        ml=GrpcServiceClient(channels["ml"], pb.ml_pb2_grpc.MLServiceStub),
        identity=GrpcServiceClient(channels["identity"], pb.identity_pb2_grpc.IdentityServiceStub),
        notifications=GrpcServiceClient(channels["notifications"], pb.notifications_pb2_grpc.NotificationServiceStub),
        op_log=GrpcOpLogReader(channels["documents"]),
    )
