#!/usr/bin/env sh
# Generates Python gRPC stubs for the ml service from proto/ into _gen/.
#
# Temporary: a parallel change adds checked-in Python stubs under gen/python
# at the repository root. When it lands, delete this script and _gen/, and
# point skein_ml.adapters.grpc_stubs at gen/python instead.
set -eu
here=$(cd "$(dirname "$0")/.." && pwd)
root=$(cd "$here/../.." && pwd)
out="$here/_gen"
rm -rf "$out"
mkdir -p "$out"
python -m grpc_tools.protoc \
  -I "$root/proto" \
  --python_out="$out" \
  --pyi_out="$out" \
  --grpc_python_out="$out" \
  "$root"/proto/skein/v1/*.proto
# protoc does not emit package markers; namespace packages would work, but
# explicit ones keep tools such as mypy and ruff from guessing.
touch "$out/skein/__init__.py" "$out/skein/v1/__init__.py"
echo "stubs written to $out"
