"""Import point for the generated Protobuf and gRPC modules.

Until the repository ships Python stubs in ``gen/python`` (a parallel
change), they are generated into ``services/ml/_gen`` by
``scripts/gen_proto.sh`` (git-ignored; the Dockerfile runs it too). This
module puts that directory on ``sys.path`` if ``skein.v1`` is not already
importable. When ``gen/python`` lands, point ``_CANDIDATES`` at it (or
install it as a package) and delete ``_gen`` and the script.

Only adapters import this module; the domain and use cases never see
generated types.
"""

from __future__ import annotations

import importlib
import os
import sys
from pathlib import Path

_SERVICE_ROOT = Path(__file__).resolve().parents[3]
_CANDIDATES = [
    os.environ.get("SKEIN_PROTO_PYTHON", ""),
    str(_SERVICE_ROOT / "_gen"),
    str(_SERVICE_ROOT.parents[1] / "gen" / "python"),
]


def _ensure_importable() -> None:
    try:
        importlib.import_module("skein.v1.ml_pb2")
        return
    except ImportError:
        pass
    for candidate in _CANDIDATES:
        if candidate and (Path(candidate) / "skein" / "v1" / "ml_pb2.py").is_file():
            sys.path.insert(0, candidate)
            return
    raise ImportError(
        "generated stubs for skein.v1 not found; run services/ml/scripts/gen_proto.sh"
    )


_ensure_importable()

from skein.v1 import events_pb2, ml_pb2, ml_pb2_grpc  # noqa: E402

__all__ = ["events_pb2", "ml_pb2", "ml_pb2_grpc"]
