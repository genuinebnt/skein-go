module github.com/genuinebnt/skein-go/services/gateway

go 1.24.0

require (
	github.com/alicebob/miniredis/v2 v2.40.0
	github.com/genuinebnt/skein-go/gen/go v0.0.0-00010101000000-000000000000
	github.com/genuinebnt/skein-go/platform v0.0.0
	github.com/redis/go-redis/v9 v9.14.0
	go.opentelemetry.io/otel v1.39.0
	go.opentelemetry.io/otel/metric v1.39.0
	go.opentelemetry.io/otel/sdk/metric v1.39.0
	google.golang.org/grpc v1.78.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.27.3 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/sdk v1.39.0 // indirect
	go.opentelemetry.io/otel/trace v1.39.0 // indirect
	golang.org/x/net v0.48.0 // indirect
	golang.org/x/sys v0.39.0 // indirect
	golang.org/x/text v0.32.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20251222181119-0a764e51fe1b // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251222181119-0a764e51fe1b // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

// gen/go and platform live in this repository. The replaces keep the
// gateway buildable with GOWORK=off (the Docker build); inside the
// workspace go.work resolves them anyway.
replace (
	github.com/genuinebnt/skein-go/gen/go => ../../gen/go
	github.com/genuinebnt/skein-go/platform => ../../platform
)
