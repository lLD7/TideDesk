#!/bin/bash
# Export only the Go release tool's source allowlist. Existing releases are
# never silently overwritten; private data and signing keys never enter Git.
set -euo pipefail
TIDE_ROOT="$(cd -- "$(dirname -- "$0")/.." && pwd)"
TIDE_REPO='lLD7/TideDesk'
TIDE_VERSION="$(go -C "$TIDE_ROOT/scripts" run ./release version)"
if [[ "$(gh api user --jq .login)" != 'lLD7' ]]; then
  echo 'Log in to GitHub as lLD7 before publishing.' >&2
  exit 1
fi
if gh release view "v$TIDE_VERSION" --repo "$TIDE_REPO" >/dev/null 2>&1; then
  echo "Release v$TIDE_VERSION already exists; increment the version instead." >&2
  exit 1
fi
for tide_asset in "TideDesk-$TIDE_VERSION-macOS-AppleSilicon.dmg" "TideDesk-$TIDE_VERSION-Source.zip" \
  "tidedesk-$TIDE_VERSION-darwin-arm64.tar.gz" update-darwin-arm64.json "TideDesk-$TIDE_VERSION-SHA256SUMS.txt"; do
  [[ -f "$TIDE_ROOT/dist/$tide_asset" ]] || { echo "Missing release artifact: $tide_asset" >&2; exit 1; }
done
TIDE_STAGE="$(mktemp -d "$TIDE_ROOT/build/tidedesk-publish.XXXXXX")"
go -C "$TIDE_ROOT/scripts" run ./release notes > "$TIDE_STAGE/notes.md"
unzip -q "$TIDE_ROOT/dist/TideDesk-$TIDE_VERSION-Source.zip" -d "$TIDE_STAGE/source"
TIDE_GIT="$TIDE_STAGE/repo"
git init -b main "$TIDE_GIT"
git -C "$TIDE_GIT" remote add origin "https://github.com/$TIDE_REPO.git"
if git -C "$TIDE_GIT" ls-remote --exit-code origin refs/heads/main >/dev/null 2>&1; then
  git -C "$TIDE_GIT" fetch origin main
  git -C "$TIDE_GIT" reset --hard FETCH_HEAD
  # Fresh temporary export checkout, never the user's workspace.
  git -C "$TIDE_GIT" rm -r --ignore-unmatch .
fi
rsync -a "$TIDE_STAGE/source/TideDesk/" "$TIDE_GIT/"
git -C "$TIDE_GIT" add .
git -C "$TIDE_GIT" -c user.name=lLD7 -c user.email=119817306+lLD7@users.noreply.github.com \
  commit -m "Release TideDesk $TIDE_VERSION"
git -C "$TIDE_GIT" -c credential.helper= -c 'credential.helper=!gh auth git-credential' push origin main
TIDE_SHA="$(git -C "$TIDE_GIT" rev-parse HEAD)"
gh release create "v$TIDE_VERSION" --repo "$TIDE_REPO" --draft --target "$TIDE_SHA" \
  --title "TideDesk $TIDE_VERSION" --notes-file "$TIDE_STAGE/notes.md"
TIDE_ASSETS=("$TIDE_ROOT/dist/TideDesk-$TIDE_VERSION-macOS-AppleSilicon.dmg"
  "$TIDE_ROOT/dist/TideDesk-$TIDE_VERSION-Source.zip"
  "$TIDE_ROOT/dist/tidedesk-$TIDE_VERSION-darwin-arm64.tar.gz"
  "$TIDE_ROOT/dist/update-darwin-arm64.json"
  "$TIDE_ROOT/dist/TideDesk-$TIDE_VERSION-SHA256SUMS.txt")
for tide_delta in "$TIDE_ROOT/dist/mygo/darwin-arm64/"*.delta; do
  [[ -f "$tide_delta" ]] && TIDE_ASSETS+=("$tide_delta")
done
gh release upload "v$TIDE_VERSION" --repo "$TIDE_REPO" "${TIDE_ASSETS[@]}"
# Publish only after every update archive, manifest and source asset exists.
gh release edit "v$TIDE_VERSION" --repo "$TIDE_REPO" --draft=false --latest
echo "Published https://github.com/$TIDE_REPO/releases/tag/v$TIDE_VERSION"
