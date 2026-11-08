#!/bin/zsh
set -eu
GEX_ROOT="$(cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$GEX_ROOT/desktop"
mkdir -p TideDesk.app/Contents/MacOS
go build -o TideDesk.app/Contents/MacOS/TideDesk .
cp Info.plist TideDesk.app/Contents/Info.plist
if [ -f assets/TideDesk.icns ]; then
  mkdir -p TideDesk.app/Contents/Resources
  cp assets/TideDesk.icns TideDesk.app/Contents/Resources/TideDesk.icns
fi
exec ./TideDesk.app/Contents/MacOS/TideDesk -root "$GEX_ROOT"
