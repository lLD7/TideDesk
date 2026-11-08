#!/usr/bin/env python3
"""Prepare distributable resources and source without local user data."""
import datetime as dt
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import zipfile

ROOT = Path(__file__).resolve().parent.parent
CONFIG = json.loads((ROOT / "desktop/mygo.json").read_text())
RESOURCES = ROOT / "build/tidedesk-resources"


def source_files():
    for dirname, suffixes in (
        ("app", {".py", ".sql", ".sha256"}),
        ("desktop", {".go", ".mod", ".sum", ".plist", ".json", ".png", ".icns", ".md"}),
        ("scripts", {".sh", ".py"}), ("tests", {".py"}),
    ):
        for path in sorted((ROOT / dirname).rglob("*")):
            relative = path.relative_to(ROOT)
            if not path.is_file() or path.suffix not in suffixes:
                continue
            if any(p.endswith(".app") or p == "__pycache__" for p in relative.parts):
                continue
            if path.name.startswith("mygo_internal"):
                continue
            yield path
    for name in ("LICENSE", "README.md", "requirements.txt", "requirements-local.lock", "pyproject.toml", ".gitignore",
                 "docs/macOS安装与打包.md", "docs/README-upstream.md", ".streamlit/config.toml"):
        path = ROOT / name
        if path.is_file():
            yield path


def main():
    if RESOURCES.exists():
        shutil.rmtree(RESOURCES)
    resources = RESOURCES / "gex"
    resources.mkdir(parents=True)
    for path in (ROOT / "app").rglob("*"):
        if path.is_file() and path.suffix in {".py", ".sql", ".sha256"} and "__pycache__" not in path.parts:
            target = resources / path.relative_to(ROOT)
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(path, target)
    runtime = ROOT / "build/python-cache/cpython-3.12.15-macos-aarch64-none"
    subprocess.run([sys.executable, str(ROOT / "scripts/bundle-python.py"), str(runtime), str(resources / "python")], check=True)
    licenses = RESOURCES / "ThirdPartyLicenses"
    licenses.mkdir()
    modules = subprocess.check_output(["go", "list", "-m", "-f", "{{.Path}}|{{.Version}}|{{.Dir}}", "all"], cwd=ROOT / "desktop", text=True)
    public_modules = []
    for record in modules.splitlines()[1:]:
        name, version, directory = record.split("|", 2)
        public_modules.append(f"{name} {version}")
        if directory:
            for path in Path(directory).iterdir():
                if path.is_file() and path.name.upper().startswith(("LICENSE", "COPYING", "NOTICE")):
                    shutil.copy2(path, licenses / (name.replace("/", "_") + "-" + path.name))
    (licenses / "go-modules.txt").write_text("\n".join(public_modules) + "\n")
    (resources / "BUILD.json").write_text(json.dumps({
        "name": "TideDesk", "version": CONFIG["version"], "architecture": "arm64", "minimum_macos": "14.0",
        "built_at": dt.datetime.now(dt.timezone.utc).isoformat(), "notarized": False,
        "update_repository": CONFIG["updates"]["github"], "update_signature": "Ed25519",
        "user_data": "~/Library/Application Support/GammaGrid",
        "validation": "Build and packaging only; no regression or market-data tests run",
    }, indent=2) + "\n")
    dist = ROOT / "dist"
    dist.mkdir(exist_ok=True)
    with zipfile.ZipFile(dist / f'TideDesk-{CONFIG["version"]}-Source.zip', "w", zipfile.ZIP_DEFLATED) as archive:
        for path in source_files():
            archive.write(path, "TideDesk/" + path.relative_to(ROOT).as_posix())
    # The secret goes only to the official builder, never to artifacts or logs.
    env = dict(os.environ)
    if not env.get("MYGO_UPDATER_PRIVATE_KEY"):
        key = Path(env.get("TIDEDESK_UPDATE_KEY", str(Path.home() / "Library/Application Support/TideDesk/update-keys/mygo-update.key")))
        env["MYGO_UPDATER_PRIVATE_KEY"] = key.read_text().strip()
    env.update(CGO_ENABLED="1", MACOSX_DEPLOYMENT_TARGET="14.0")
    command = ["go", "run", "github.com/egoist/mygo/cmd/mygo", "build", "-platform", "darwin/arm64", "-skip-dmg"]
    if env.get("TIDEDESK_SIGN_IDENTITY"):
        command.extend(["-sign", env["TIDEDESK_SIGN_IDENTITY"]])
    subprocess.run(command, cwd=ROOT / "desktop", env=env, check=True)


if __name__ == "__main__":
    main()
