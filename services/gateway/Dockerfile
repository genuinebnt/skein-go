# syntax=docker/dockerfile:1.7
# gateway image. Build from the repository root (the module uses gen/go
# and platform through replace directives):
#   docker build -f services/gateway/Dockerfile -t skein/gateway .

ARG GO_IMAGE=golang:1.24.7-bookworm

FROM ${GO_IMAGE} AS build
ENV CGO_ENABLED=0 GOWORK=off
WORKDIR /src
COPY gen/go/go.mod gen/go/go.sum gen/go/
COPY platform/go.mod platform/go.sum platform/
COPY services/gateway/go.mod services/gateway/go.sum services/gateway/
RUN --mount=type=cache,target=/go/pkg/mod cd services/gateway && go mod download
COPY gen/go gen/go
COPY platform platform
COPY services/gateway services/gateway
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    cd services/gateway && go build -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/gateway

# distroless static: no shell, no package manager, runs as uid 65532.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gateway /gateway
USER nonroot:nonroot
EXPOSE 8080
HEALTHCHECK --interval=5s --timeout=3s --retries=10 CMD ["/gateway", "healthcheck"]
ENTRYPOINT ["/gateway"]
