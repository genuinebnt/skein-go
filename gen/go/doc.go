// Package gen is the Go module holding code generated from proto/skein/v1.
//
// Everything under skein/v1 is produced by `make proto` (buf generate with
// the plugin versions pinned in buf.gen.yaml) and committed, so services can
// depend on the contracts without running protoc. Do not edit generated
// files: change the .proto files and regenerate. CI regenerates and fails if
// the committed output differs.
//
// Package skeinv1 contains the messages (protoc-gen-go), gRPC clients and
// servers (protoc-gen-go-grpc) and REST handlers for the gateway
// (protoc-gen-grpc-gateway). The contracttest package checks the REST
// mapping, the OpenAPI document and the API docs against each other.
//
// Porting notes: the Rust port generates its own code from the same .proto
// files with prost and tonic; nothing here is shared with it.
package gen
