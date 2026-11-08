#!/bin/bash
# Build signed MyGo updates and a first-install DMG; no app launch or tests.
set -euo pipefail
TIDE_ROOT="$(cd -- "$(dirname -- "$0")/.." && pwd)"
if [[ "$(uname -s)" != Darwin || "$(uname -m)" != arm64 ]]; then
  echo 'Build this edition on an Apple Silicon Mac.' >&2
  exit 1
fi
TIDE_VERSION="$(go -C "$TIDE_ROOT/scripts" run ./release version)"
mkdir -p "$TIDE_ROOT/build" "$TIDE_ROOT/dist"
uv python install 3.12.15 --install-dir "$TIDE_ROOT/build/python-cache" --no-bin --no-config
go -C "$TIDE_ROOT/scripts" run ./release build
TIDE_OUTPUT="$TIDE_ROOT/dist/mygo/darwin-arm64"
TIDE_STAGE="$(mktemp -d "$TIDE_ROOT/build/tidedesk-dmg.XXXXXX")"
ditto "$TIDE_OUTPUT/TideDesk.app" "$TIDE_STAGE/TideDesk.app"
ln -s /Applications "$TIDE_STAGE/Applications"
cp "$TIDE_ROOT/LICENSE" "$TIDE_STAGE/LICENSE.txt"
cp "$TIDE_ROOT/docs/macOS安装与打包.md" "$TIDE_STAGE/安装说明.md"
cp "$TIDE_ROOT/dist/TideDesk-$TIDE_VERSION-Source.zip" "$TIDE_STAGE/TideDesk-Source.zip"
hdiutil create -volname TideDesk -srcfolder "$TIDE_STAGE" -format UDZO \
  -imagekey zlib-level=9 -ov "$TIDE_ROOT/dist/TideDesk-$TIDE_VERSION-macOS-AppleSilicon.dmg"
cp "$TIDE_OUTPUT/update-darwin-arm64.json" "$TIDE_ROOT/dist/"
cp "$TIDE_OUTPUT/tidedesk-$TIDE_VERSION-darwin-arm64.tar.gz" "$TIDE_ROOT/dist/"
for tide_delta in "$TIDE_OUTPUT/"*.delta; do
  [[ -f "$tide_delta" ]] && cp "$tide_delta" "$TIDE_ROOT/dist/"
done
cp "$TIDE_ROOT/docs/macOS安装与打包.md" "$TIDE_ROOT/dist/安装说明.md"
(
  cd "$TIDE_ROOT/dist"
  shasum -a 256 "TideDesk-$TIDE_VERSION-macOS-AppleSilicon.dmg" "TideDesk-$TIDE_VERSION-Source.zip" \
    "tidedesk-$TIDE_VERSION-darwin-arm64.tar.gz" update-darwin-arm64.json > "TideDesk-$TIDE_VERSION-SHA256SUMS.txt"
)
echo "Created TideDesk $TIDE_VERSION: $TIDE_ROOT/dist/"
