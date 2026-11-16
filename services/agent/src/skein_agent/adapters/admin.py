"""HTTP admin endpoints for probes, beside the gRPC port (the backends'
convention: gRPC on 9090, admin HTTP on 8080).

``GET /healthz`` answers 200 while the process is up; ``GET /readyz`` answers
200 while the service accepts work and 503 once shutdown has begun, so a load
balancer drains it first. Metrics leave over OTLP (docs/observability.md).
A few lines on ``asyncio.start_server``: two probe endpoints need no
framework, and only the request line is read.
"""

from __future__ import annotations

import asyncio
from collections.abc import Callable

_REASONS = {200: "OK", 404: "Not Found", 405: "Method Not Allowed", 503: "Service Unavailable"}


async def start_admin(address: str, ready: Callable[[], bool]) -> asyncio.Server:
    """Serve ``/healthz`` and ``/readyz`` on ``address`` ("host:port")."""
    host, _, port = address.rpartition(":")

    async def handle(reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
        try:
            request_line = await asyncio.wait_for(reader.readline(), 5)
            parts = request_line.decode("latin-1").split()
            status, body = _route(parts[0] if parts else "", parts[1] if len(parts) > 1 else "", ready)
            data = body.encode()
            head = (
                f"HTTP/1.1 {status} {_REASONS[status]}\r\nContent-Type: text/plain; charset=utf-8\r\n"
                f"Content-Length: {len(data)}\r\nConnection: close\r\n\r\n"
            )
            writer.write(head.encode() + data)
            await writer.drain()
        except (TimeoutError, ConnectionError):
            pass
        finally:
            writer.close()

    return await asyncio.start_server(handle, host.strip("[]") or None, int(port))


def _route(method: str, path: str, ready: Callable[[], bool]) -> tuple[int, str]:
    if method != "GET":
        return 405, "method not allowed\n"
    if path == "/healthz":
        return 200, "ok\n"
    if path == "/readyz":
        return (200, "ready\n") if ready() else (503, "draining\n")
    return 404, "not found\n"
