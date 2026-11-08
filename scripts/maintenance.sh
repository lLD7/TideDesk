#!/bin/bash
set -euo pipefail
TIDE_ROOT="$(cd -- "$(dirname -- "$0")/.." && pwd)"
exec go -C "$TIDE_ROOT/desktop" run . maintenance -root "$TIDE_ROOT" -cwd "$PWD" "$@"
