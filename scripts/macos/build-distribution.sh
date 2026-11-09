#!/bin/bash
# No tool installation, signing identity, keychain or privileged access is used.
set -euo pipefail
NODE_TOOL="$(command -v node || true)"
args=("$@")
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --node) [[ "$#" -ge 2 ]] || { echo '--node requires an absolute executable' >&2; exit 2; }; NODE_TOOL="$2"; shift 2 ;;
    *) shift ;;
  esac
done
[[ "$NODE_TOOL" == /* && -x "$NODE_TOOL" ]] || { echo 'Node 24 is required; use --node /absolute/node' >&2; exit 2; }
exec "$NODE_TOOL" "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/distribution.mjs" "${args[@]}"
