module github.com/genuinebnt/skein-go/e2e

go 1.24.0

require (
	github.com/coder/websocket v1.8.15
	github.com/genuinebnt/skein-go/gen/go v0.0.0-00010101000000-000000000000
	github.com/genuinebnt/skein-go/libs/crdt v0.0.0-00010101000000-000000000000
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251222181119-0a764e51fe1b
	google.golang.org/grpc v1.68.0
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.24.0 // indirect
	golang.org/x/net v0.29.0 // indirect
	golang.org/x/sys v0.25.0 // indirect
	golang.org/x/text v0.20.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20241118233622-e639e219e697 // indirect
)

// Sibling modules of this repository. The replaces keep the module buildable
// with GOWORK=off; go.work points at the same directories.
replace (
	github.com/genuinebnt/skein-go/gen/go => ../gen/go
	github.com/genuinebnt/skein-go/libs/crdt => ../libs/crdt
)
