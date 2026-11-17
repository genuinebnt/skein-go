#!/usr/bin/env bash
# The justfile's shell: runs a recipe line in bash (errexit, nounset,
# pipefail) with the tools pinned in mise.toml first on PATH, whether or
# not mise is activated in the caller's shell (lib.sh, use_pinned_tools).
# That way `just test` builds with the same Go as `just dev`.
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
use_pinned_tools
exec bash -euo pipefail "$@"
