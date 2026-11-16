"""Access to the generated skein.v1 protobuf modules.

Everything in this package imports protobuf types from here, so where the
stubs come from is decided in one place.

For now the stubs are generated into ``services/agent/gen`` (git-ignored) by
``scripts/gen_proto.py``. A parallel change adds generated Python under the
repository's ``gen/python``; when it lands, install that as a package, drop
the path fallback below, and keep the imports unchanged.
"""

from __future__ import annotations

import importlib
import sys
from pathlib import Path
from types import ModuleType

_LOCAL_GEN = Path(__file__).resolve().parents[2] / "gen"


def _import(name: str) -> ModuleType:
    try:
        return importlib.import_module(name)
    except ModuleNotFoundError:
        if _LOCAL_GEN.is_dir() and str(_LOCAL_GEN) not in sys.path:
            sys.path.append(str(_LOCAL_GEN))
            return importlib.import_module(name)
        raise ModuleNotFoundError(
            f"{name} is not importable. Generate the stubs with "
            "`python scripts/gen_proto.py` (in services/agent) or install gen/python."
        ) from None


agent_pb2 = _import("skein.v1.agent_pb2")
agent_pb2_grpc = _import("skein.v1.agent_pb2_grpc")
common_pb2 = _import("skein.v1.common_pb2")
documents_pb2 = _import("skein.v1.documents_pb2")
documents_pb2_grpc = _import("skein.v1.documents_pb2_grpc")
identity_pb2 = _import("skein.v1.identity_pb2")
identity_pb2_grpc = _import("skein.v1.identity_pb2_grpc")
ml_pb2 = _import("skein.v1.ml_pb2")
ml_pb2_grpc = _import("skein.v1.ml_pb2_grpc")
notifications_pb2 = _import("skein.v1.notifications_pb2")
notifications_pb2_grpc = _import("skein.v1.notifications_pb2_grpc")
search_pb2 = _import("skein.v1.search_pb2")
search_pb2_grpc = _import("skein.v1.search_pb2_grpc")

# A FileDescriptorSet with source info, written next to the stubs. Used only
# for tool descriptions; absent is fine.
DESCRIPTOR_SET_PATH = _LOCAL_GEN / "skein.binpb"
