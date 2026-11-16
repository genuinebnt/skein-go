# syntax=docker/dockerfile:1
# Build from the repository root:
#   docker build -f services/search/Dockerfile .
# The service depends on sibling modules (gen/go, libs, platform) through
# replace directives, so the whole set is copied and built with GOWORK=off.

FROM golang:1.24.7-alpine AS build
ENV CGO_ENABLED=0 GOWORK=off
WORKDIR /src
COPY gen/go gen/go
COPY libs libs
COPY platform platform
COPY services/search/go.mod services/search/go.sum services/search/
WORKDIR /src/services/search
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY services/search ./
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o /out/search ./cmd/search && \
    go build -trimpath -ldflags="-s -w" -o /out/search-seed ./cmd/search-seed

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/search /out/search-seed /usr/local/bin/
USER nonroot:nonroot
EXPOSE 9090 8080
ENTRYPOINT ["/usr/local/bin/search"]
