# syntax=docker/dockerfile:1.7
# Agent service image. Build from the repository root, because the stubs are
# generated from proto/:
#
#   docker build -f services/agent/Dockerfile -t skein-agent .
#
# Stage 1 resolves dependencies with uv from uv.lock and generates the gRPC
# stubs; stage 2 is a slim runtime with only the virtualenv, the stubs and
# the code, running as an unprivileged user.

FROM python:3.13-slim AS build
COPY --from=ghcr.io/astral-sh/uv:0.9 /uv /usr/local/bin/uv
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy UV_PROJECT_ENVIRONMENT=/opt/venv
WORKDIR /src/services/agent

# Dependencies first, so code changes do not invalidate this layer.
COPY services/agent/pyproject.toml services/agent/uv.lock ./
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-dev --extra postgres --no-install-project

# Stubs: generated here until gen/python is published as a package.
COPY proto /src/proto
COPY services/agent/scripts ./scripts
RUN --mount=type=cache,target=/root/.cache/uv \
    uv run --no-project --with "grpcio-tools>=1.85" python scripts/gen_proto.py

COPY services/agent/README.md ./
COPY services/agent/src ./src
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-dev --extra postgres --no-editable

FROM python:3.13-slim AS runtime
RUN groupadd --system --gid 10001 skein \
 && useradd --system --uid 10001 --gid skein --no-create-home --shell /usr/sbin/nologin skein
COPY --from=build /opt/venv /opt/venv
COPY --from=build /src/services/agent/gen /app/gen
ENV PATH=/opt/venv/bin:$PATH \
    PYTHONPATH=/app/gen \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    SKEIN_AGENT_ADDR=0.0.0.0:50057
USER 10001:10001
EXPOSE 50057
# Kubernetes probes use the gRPC health service (skein.v1.AgentService).
ENTRYPOINT ["python", "-m", "skein_agent"]
