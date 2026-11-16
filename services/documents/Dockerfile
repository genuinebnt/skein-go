# syntax=docker/dockerfile:1.7
# documents service image. Build from the repository root (the module uses
# its sibling modules through replace directives):
#   docker build -f services/documents/Dockerfile -t skein/documents .
# The image holds two binaries: documents (the service; `documents
# healthcheck` probes its readiness endpoint) and documents-seed.

ARG GO_IMAGE=golang:1.24.7-alpine

FROM ${GO_IMAGE} AS build
ENV CGO_ENABLED=0 GOWORK=off GOTOOLCHAIN=local GOFLAGS=-trimpath
WORKDIR /src
COPY gen/go/go.mod gen/go/go.sum gen/go/
COPY platform/go.mod platform/go.sum platform/
COPY libs/crdt/go.mod libs/crdt/
COPY libs/dp/go.mod libs/dp/
COPY libs/blocks/go.mod libs/blocks/
COPY services/documents/go.mod services/documents/go.sum services/documents/
WORKDIR /src/services/documents
RUN --mount=type=cache,target=/go/pkg/mod go mod download
WORKDIR /src
COPY gen/go gen/go
COPY platform platform
COPY libs/crdt libs/crdt
COPY libs/dp libs/dp
COPY libs/blocks libs/blocks
COPY services/documents services/documents
WORKDIR /src/services/documents
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags="-s -w" -o /out/documents ./cmd/documents \
 && go build -ldflags="-s -w" -o /out/documents-seed ./cmd/documents-seed

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
ENV SKEIN_VERSION=${VERSION}
COPY --from=build /out/documents /out/documents-seed /usr/local/bin/
USER nonroot:nonroot
EXPOSE 9090 8080
HEALTHCHECK --interval=10s --timeout=4s --start-period=20s --retries=3 CMD ["/usr/local/bin/documents", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/documents"]
