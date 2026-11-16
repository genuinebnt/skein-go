# syntax=docker/dockerfile:1.7
# notifications image. Build from the repository root (the module uses
# gen/go and platform through replace directives):
#   docker build -f services/notifications/Dockerfile -t skein/notifications .
# Migrations and email templates are embedded in the binary.

ARG GO_IMAGE=golang:1.24.7-bookworm

FROM ${GO_IMAGE} AS build
ENV CGO_ENABLED=0 GOWORK=off
WORKDIR /src
COPY gen/go/go.mod gen/go/go.sum gen/go/
COPY platform/go.mod platform/go.sum platform/
COPY services/notifications/go.mod services/notifications/go.sum services/notifications/
RUN --mount=type=cache,target=/go/pkg/mod cd services/notifications && go mod download
COPY gen/go gen/go
COPY platform platform
COPY services/notifications services/notifications
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    cd services/notifications && go build -trimpath -ldflags="-s -w" -o /out/notifications ./cmd/notifications

# distroless static: no shell, no package manager, runs as uid 65532.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/notifications /notifications
USER nonroot:nonroot
# 9090 gRPC (NotificationService), 8080 HTTP admin (/healthz, /readyz).
EXPOSE 9090 8080
HEALTHCHECK --interval=5s --timeout=3s --retries=10 CMD ["/notifications", "healthcheck"]
# SIGTERM drains: gRPC first, then consumers, the email sender and clients.
STOPSIGNAL SIGTERM
ENTRYPOINT ["/notifications"]
