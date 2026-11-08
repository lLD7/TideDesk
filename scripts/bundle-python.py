#!/usr/bin/env python3
"""Copy the validated Python 3.12 dependency closure into a portable runtime.

Run with the checkout's .venv interpreter. No application code is executed.
The standalone CPython distribution supplies the interpreter and stdlib; only
Yahoo/RV helper dependencies are copied, together with their package licenses.
"""
import argparse
from importlib import metadata
import json
from pathlib import Path
import shutil
import sys

from packaging.requirements import Requirement
from packaging.utils import canonicalize_name


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("runtime", type=Path)
    parser.add_argument("destination", type=Path)
    args = parser.parse_args()
    if sys.version_info[:2] != (3, 12):
        raise SystemExit("Package the Yahoo helper using the validated Python 3.12 .venv")
    shutil.copytree(args.runtime, args.destination, symlinks=True,
                    ignore=shutil.ignore_patterns("__pycache__", "*.pyc"))
    target = args.destination / "lib/python3.12/site-packages"
    target.mkdir(parents=True, exist_ok=True)
    pending = [(name, set()) for name in (
        "numpy", "pandas", "scipy", "yfinance", "requests", "exchange-calendars", "tzdata"
    )]
    seen = {}
    versions = {}
    while pending:
        name, extras = pending.pop()
        name = canonicalize_name(name)
        if name in seen and extras <= seen[name]:
            continue
        seen.setdefault(name, set()).update(extras)
        dist = metadata.distribution(name)
        if name not in versions:
            source = Path(dist.locate_file("")).resolve()
            for item in dist.files or []:
                relative = Path(str(item))
                # RECORD also lists entry-point scripts outside site-packages.
                # The app invokes python -m and does not need those scripts.
                if relative.is_absolute() or ".." in relative.parts or "__pycache__" in relative.parts:
                    continue
                if relative.suffix == ".pyc":
                    continue
                src = source / relative
                if not src.is_file():
                    continue
                dest = target / relative
                dest.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(src, dest)
            versions[name] = dist.version
        for raw in dist.requires or []:
            req = Requirement(raw)
            if not req.marker or any(req.marker.evaluate({"extra": extra}) for extra in seen[name] | {""}):
                pending.append((req.name, set(req.extras)))
    manifest = {"python_runtime": args.runtime.name, "packages": dict(sorted(versions.items())),
                "source": "Validated checkout .venv; package files copied without executing application code"}
    (args.destination / "tidedesk-runtime.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print(f"Bundled portable Python and {len(versions)} helper dependencies", flush=True)


if __name__ == "__main__":
    main()
