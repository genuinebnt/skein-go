# syntax=docker/dockerfile:1.7
# Agent service image. Build from the repository root, because the generated
# stubs come from gen/python:
#
#   docker build -f services/agent/Dockerfile -t skein-agent .
#
# Stage 1 resolves dependencies with uv from uv.lock, including gen/python
# (a path dependency); stage 2 is a slim runtime with only the virtualenv,
# running as an unprivileged user.

FROM python:3.13-slim AS build
COPY --from=ghcr.io/astral-sh/uv:0.9 /uv /usr/local/bin/uv
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy UV_PROJECT_ENVIRONMENT=/opt/venv
WORKDIR /src/services/agent

# Dependencies first, so code changes do not invalidate this layer. The
# stubs keep their relative place (../../gen/python) because uv.lock names
# that path.
COPY gen/python /src/gen/python
COPY services/agent/pyproject.toml services/agent/uv.lock ./
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-dev --extra postgres --no-install-project --no-editable

COPY services/agent/README.md ./
COPY services/agent/src ./src
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-dev --extra postgres --no-editable

FROM python:3.13-slim AS runtime
RUN groupadd --system --gid 10001 skein \
 && useradd --system --uid 10001 --gid skein --no-create-home --shell /usr/sbin/nologin skein
COPY --from=build /opt/venv /opt/venv
COPY services/agent/migrations /app/migrations
ENV PATH=/opt/venv/bin:$PATH \
    SKEIN_AGENT_MIGRATIONS_DIR=/app/migrations \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    SKEIN_AGENT_ADDR=0.0.0.0:9090 \
    SKEIN_AGENT_ADMIN_ADDR=0.0.0.0:8080
USER 10001:10001
EXPOSE 9090 8080
# Kubernetes probes use the gRPC health service (skein.v1.AgentService).
HEALTHCHECK --interval=10s --timeout=3s --start-period=15s --retries=3 \
  CMD ["python", "-m", "skein_agent.healthcheck"]
STOPSIGNAL SIGTERM
ENTRYPOINT ["python", "-m", "skein_agent"]
