#!/bin/sh
# Builds the browser CRDT module (ADR-0007) into web/public/wasm/:
#   skein-crdt.wasm  the module
#   wasm_exec.js     the Go runtime glue that matches the compiler used
#
# SKEIN_WASM_COMPILER=tinygo|go picks the compiler. The default is TinyGo
# when it is installed (much smaller output, the production target) and
# standard Go otherwise. --if-missing skips the build when the module exists.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
out="$here/../public/wasm"

if [ "${1:-}" = "--if-missing" ] && [ -f "$out/skein-crdt.wasm" ] && [ -f "$out/wasm_exec.js" ]; then
  exit 0
fi

# A local Go environment file (proxy settings for module downloads) is
# honoured when it exists; nothing here needs the network otherwise.
[ -f "$HOME/.skein-env" ] && . "$HOME/.skein-env"

compiler=${SKEIN_WASM_COMPILER:-}
if [ -z "$compiler" ]; then
  if command -v tinygo >/dev/null 2>&1; then compiler=tinygo; else compiler=go; fi
fi

mkdir -p "$out"
cd "$here"
case "$compiler" in
  tinygo)
    # -no-debug drops DWARF; -opt=2 favours speed over the last few KiB.
    # TinyGo runs main as a goroutine with a fixed stack; libs/crdt's
    # CheckInvariants walks the Fugue tree recursively, and a long typed run
    # makes that tree as deep as the run, so the stack is raised from the
    # default to 1 MiB (only debug checks need it; edits never recurse).
    GOWORK=off tinygo build -o "$out/skein-crdt.wasm" -target wasm -no-debug -opt=2 -stack-size=1MB ./cmd/skeincrdt
    cp "$(tinygo env TINYGOROOT)/targets/wasm_exec.js" "$out/wasm_exec.js"
    ;;
  go)
    GOWORK=off GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o "$out/skein-crdt.wasm" ./cmd/skeincrdt
    cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$out/wasm_exec.js"
    ;;
  *)
    echo "unknown SKEIN_WASM_COMPILER: $compiler" >&2
    exit 2
    ;;
esac
echo "$compiler" > "$out/COMPILER"
size=$(wc -c < "$out/skein-crdt.wasm")
echo "built web/public/wasm/skein-crdt.wasm with $compiler ($size bytes)"
