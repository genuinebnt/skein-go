# Vendored third-party protos

These files are copied verbatim from their upstream projects so that `buf
generate` works offline and does not depend on the Buf Schema Registry. Do not
edit them; to upgrade, copy the new versions over and update this table and
the plugin versions in `buf.gen.yaml`, the `Makefile` and CI together.

| Files | Upstream | Version | Licence |
|---|---|---|---|
| `google/api/annotations.proto`, `google/api/http.proto`, `google/api/field_behavior.proto` | [googleapis/googleapis](https://github.com/googleapis/googleapis) | commit `62746790cb5c0fe1956e7f2df8422d3a7f00ba15` (2026-11-15) | Apache License 2.0 (`google/LICENSE`) |
| `protoc-gen-openapiv2/options/annotations.proto`, `protoc-gen-openapiv2/options/openapiv2.proto` | [grpc-ecosystem/grpc-gateway](https://github.com/grpc-ecosystem/grpc-gateway) | `v2.24.0` (commit `a5f1b64039b6e6aa1ab9bccc1cd31f4342c5ecc3`), taken from the Go module cache after `go mod download github.com/grpc-ecosystem/grpc-gateway/v2@v2.24.0` | BSD 3-Clause (`protoc-gen-openapiv2/LICENSE`) |

How they were fetched:

```sh
git clone --depth 1 --filter=blob:none --sparse https://github.com/googleapis/googleapis.git
git -C googleapis sparse-checkout set google/api
go mod download github.com/grpc-ecosystem/grpc-gateway/v2@v2.24.0
```

Generated code for these files is not produced here: Go imports it from
`google.golang.org/genproto/googleapis/api` and
`github.com/grpc-ecosystem/grpc-gateway/v2`, and Python takes `google.api`
from `googleapis-common-protos`. The openapiv2 options have no maintained
Python package, so `scripts/gen-python.sh` generates them inside
`skein_proto`.

The openapiv2 options version must match `protoc-gen-openapiv2`, and the
generated Go must match the `grpc-gateway/v2` runtime in `gen/go/go.mod`.
