# syntax=docker/dockerfile:1.7
# ml service image. Build from the repository root (it installs gen/python):
#   docker build -f services/ml/Dockerfile -t skein/ml .
# EXTRAS selects optional adapters; add "onnx" to bundle ONNX Runtime (the
# model itself is mounted at runtime and named by SKEIN_ML_ONNX_MODEL_DIR).

ARG PYTHON_IMAGE=python:3.12-slim-bookworm

FROM ${PYTHON_IMAGE} AS build
ARG EXTRAS=redis,postgres,pubsub
ENV PIP_NO_CACHE_DIR=1 PIP_DISABLE_PIP_VERSION_CHECK=1
WORKDIR /src
RUN python -m venv /opt/venv
ENV PATH=/opt/venv/bin:$PATH
# The generated stubs and skein-events are path dependencies (../../gen/python
# and ../../platform/events/python from the service), so they keep the same
# relative places here.
COPY gen/python gen/python
COPY platform/events/python platform/events/python
COPY services/ml/pyproject.toml services/ml/README.md services/ml/
COPY services/ml/src services/ml/src
RUN pip install "./services/ml[${EXTRAS}]"

FROM ${PYTHON_IMAGE} AS runtime
RUN groupadd --system --gid 10001 skein \
 && useradd --system --uid 10001 --gid skein --no-create-home --shell /usr/sbin/nologin skein
COPY --from=build /opt/venv /opt/venv
COPY services/ml/migrations /app/migrations
ENV PATH=/opt/venv/bin:$PATH \
    PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    SKEIN_ML_MIGRATIONS_DIR=/app/migrations \
    SKEIN_ML_ADDRESS=[::]:9090 \
    SKEIN_ML_ADMIN_ADDRESS=0.0.0.0:8080
USER 10001:10001
EXPOSE 9090 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=3 \
  CMD ["python", "-m", "skein_ml.healthcheck"]
# SIGTERM triggers the graceful drain in skein_ml.main.
STOPSIGNAL SIGTERM
ENTRYPOINT ["skein-ml"]
