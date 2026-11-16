"""``python -m skein_agent.healthcheck``: exit 0 if the local server reports
SERVING for ``skein.v1.AgentService``.

For container healthchecks (compose); Kubernetes uses its native gRPC probe
against the same health service instead.
"""

from __future__ import annotations

import os
import sys

import grpc
from grpc_health.v1 import health_pb2, health_pb2_grpc


def main() -> int:
    """Query ``grpc.health.v1.Health/Check`` on the configured port."""
    port = os.environ.get("SKEIN_AGENT_ADDR", "0.0.0.0:9090").rsplit(":", 1)[-1]
    try:
        with grpc.insecure_channel(f"127.0.0.1:{port}") as channel:
            response = health_pb2_grpc.HealthStub(channel).Check(
                health_pb2.HealthCheckRequest(service="skein.v1.AgentService"), timeout=2
            )
    except grpc.RpcError as error:
        print(f"unhealthy: {error.code().name}", file=sys.stderr)
        return 1
    return 0 if response.status == health_pb2.HealthCheckResponse.SERVING else 1


if __name__ == "__main__":
    sys.exit(main())
