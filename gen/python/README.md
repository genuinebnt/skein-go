# skein-proto

Generated Python bindings for Skein's gRPC contracts in `proto/skein/v1`:
messages (`*_pb2.py`), type stubs (`*_pb2.pyi`) and gRPC clients and
servicers (`*_pb2_grpc.py`), plus `skein_proto/v1/descriptor_set.binpb`, a
`FileDescriptorSet` of `skein/v1` with source info (the generated modules
drop comments; the agent reads its tool descriptions from this file). Used
by the `ml` and `agent` services, which depend on this directory by path.

```sh
pip install -e gen/python          # from the repository root
```

```python
from skein_proto.v1 import search_pb2, search_pb2_grpc
```

Do not edit the generated files. Change the `.proto` files and run
`make proto`, which calls `scripts/gen-python.sh` (it needs
`grpcio-tools==1.85.1`). CI fails if the committed output is stale.

`tests/` holds smoke tests: `pip install -e 'gen/python[test]' && pytest gen/python`.
