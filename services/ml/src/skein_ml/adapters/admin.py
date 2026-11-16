"""HTTP admin endpoints for probes, beside the gRPC port (the backends'
convention: gRPC on 9090, admin HTTP on 8080).

``GET /healthz`` answers 200 while the process is up; ``GET /readyz`` answers
200 while the service accepts work and 503 once shutdown has begun, so a load
balancer drains it first. Metrics leave over OTLP (docs/observability.md),
not from here. A standard-library threading server: two tiny endpoints need
no framework.
"""

from __future__ import annotations

import threading
from collections.abc import Callable
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

__all__ = ["AdminServer"]


class AdminServer:
    """Serves ``/healthz`` and ``/readyz`` on ``address`` ("host:port")."""

    def __init__(self, address: str, ready: Callable[[], bool]):
        host, _, port = address.rpartition(":")
        host = host.strip("[]")

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self) -> None:
                if self.path == "/healthz":
                    self._reply(200, "ok\n")
                elif self.path == "/readyz":
                    self._reply(*((200, "ready\n") if ready() else (503, "draining\n")))
                else:
                    self._reply(404, "not found\n")

            def _reply(self, status: int, body: str) -> None:
                data = body.encode()
                self.send_response(status)
                self.send_header("Content-Type", "text/plain; charset=utf-8")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def log_message(self, *args) -> None:
                """Probes every few seconds would drown the JSON logs."""

        bind = "" if host in ("", "::", "0.0.0.0") else host
        self._server = ThreadingHTTPServer((bind, int(port)), Handler)
        self._thread: threading.Thread | None = None

    @property
    def port(self) -> int:
        """The bound port (useful with port 0)."""
        return self._server.server_address[1]

    def start(self) -> None:
        """Serve in a daemon thread."""
        self._thread = threading.Thread(
            target=self._server.serve_forever, name="admin-http", daemon=True
        )
        self._thread.start()

    def stop(self) -> None:
        """Stop serving and close the socket."""
        self._server.shutdown()
        self._server.server_close()
