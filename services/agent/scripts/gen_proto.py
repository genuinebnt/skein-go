"""Generate the Python gRPC stubs for skein.v1 into services/agent/gen/.

The output directory is git-ignored. A parallel change adds checked-in stubs
under the repository's gen/python; once it lands, delete this script and the
fallback in skein_agent/pb.py, and depend on that package instead.

Besides the *_pb2 modules this writes gen/skein.binpb, a FileDescriptorSet
with source info, so tool descriptions can be read from the proto comments.

Usage: python scripts/gen_proto.py   (needs grpcio-tools)
"""

from __future__ import annotations

import sys
from pathlib import Path

SERVICE_ROOT = Path(__file__).resolve().parent.parent
REPO_ROOT = SERVICE_ROOT.parent.parent
PROTO_ROOT = REPO_ROOT / "proto"
OUT = SERVICE_ROOT / "gen"


def main() -> int:
    from grpc_tools import protoc  # imported here so the module imports without the dev extra

    protos = sorted(str(p) for p in (PROTO_ROOT / "skein" / "v1").glob("*.proto"))
    if not protos:
        print(f"no .proto files under {PROTO_ROOT}", file=sys.stderr)
        return 1
    OUT.mkdir(exist_ok=True)
    well_known = Path(protoc.__file__).parent / "_proto"
    args = [
        "protoc",
        f"-I{PROTO_ROOT}",
        f"-I{well_known}",
        f"--python_out={OUT}",
        f"--pyi_out={OUT}",
        f"--grpc_python_out={OUT}",
        f"--descriptor_set_out={OUT / 'skein.binpb'}",
        "--include_source_info",
        "--include_imports",
        *protos,
    ]
    code = protoc.main(args)
    if code == 0:
        print(f"generated {len(protos)} files into {OUT}")
    return code


if __name__ == "__main__":
    raise SystemExit(main())
