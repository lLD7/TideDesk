#!/bin/zsh
set -eu
GEX_ROOT="$(cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$GEX_ROOT"
if [[ ! -x .venv/bin/python ]]; then
  print -u2 "Missing .venv. Follow the local setup steps in README.md."
  exit 1
fi
export PYTHONPATH="$GEX_ROOT${PYTHONPATH:+:$PYTHONPATH}"
exec "$GEX_ROOT/.venv/bin/python" -m app.launcher "$@"
