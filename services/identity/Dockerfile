# syntax=docker/dockerfile:1.7
# identity service image. Build from the repository root (the module uses
# the sibling platform and gen/go modules through replace directives):
#   docker build -f services/identity/Dockerfile -t skein/identity .
# The image holds two binaries: identity (the service; `identity
# healthcheck` probes its readiness endpoint) and identity-seed.

ARG GO_IMAGE=golang:1.24.7-alpine

FROM ${GO_IMAGE} AS build
ENV CGO_ENABLED=0 GOWORK=off GOTOOLCHAIN=local GOFLAGS=-trimpath
WORKDIR /src
COPY gen/go/go.mod gen/go/go.sum gen/go/
COPY platform/go.mod platform/go.sum platform/
COPY services/identity/go.mod services/identity/go.sum services/identity/
WORKDIR /src/services/identity
RUN --mount=type=cache,target=/go/pkg/mod go mod download
WORKDIR /src
COPY gen/go gen/go
COPY platform platform
COPY services/identity services/identity
WORKDIR /src/services/identity
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags="-s -w" -o /out/identity ./cmd/identity \
 && go build -ldflags="-s -w" -o /out/identity-seed ./cmd/identity-seed

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
ENV SKEIN_VERSION=${VERSION}
COPY --from=build /out/identity /out/identity-seed /usr/local/bin/
USER nonroot:nonroot
EXPOSE 9090 8080
HEALTHCHECK --interval=10s --timeout=4s --start-period=20s --retries=3 CMD ["/usr/local/bin/identity", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/identity"]
