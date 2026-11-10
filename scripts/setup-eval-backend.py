#!/usr/bin/env python3
"""Provision an explicitly selected, trusted beads_rust for private evals only."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

VERSION = "br 0.7.6"


def main():
    if len(sys.argv) != 2 or not Path(sys.argv[1]).is_absolute():
        raise ValueError("usage: scripts/setup-eval-backend.py /absolute/path/to/trusted/beads_rust")
    source = Path(sys.argv[1])
    if not source.is_file() or not os.access(source, os.X_OK):
        raise ValueError(f"unavailable executable: {source}")
    # Capture the bytes first, then validate the private copy we will actually use.
    body = source.read_bytes()
    digest = hashlib.sha256(body).hexdigest()
    default_cache = Path.home() / ("Library/Caches" if sys.platform == "darwin" else ".cache")
    cache = Path(str(default_cache) if sys.platform == "darwin" else os.environ.get("XDG_CACHE_HOME", str(default_cache)))
    if not cache.is_absolute():
        raise ValueError("XDG_CACHE_HOME must be absolute")
    root = cache / "commander" / "eval-tools"
    root.mkdir(parents=True, exist_ok=True, mode=0o700)
    with tempfile.NamedTemporaryFile(dir=root, delete=False) as f:
        pending = Path(f.name)
        f.write(body)
    try:
        pending.chmod(0o700)
        version = subprocess.check_output([str(pending), "--version"], timeout=5, text=True).strip()
        if version != VERSION:
            raise ValueError(f"expected {VERSION}, got {version!r}")
        backend = root / ("br-0.7.6-" + digest)
        pending.replace(backend)
        record = {"schema": "eval-backend@v1", "path": str(backend), "version": version, "sha256": digest}
        with tempfile.NamedTemporaryFile(mode="w", dir=root, delete=False) as f:
            pending = Path(f.name)
            json.dump(record, f)
            f.write("\n")
        pending.replace(root / "backend.json")
        print(json.dumps(record))
    finally:
        pending.unlink(missing_ok=True)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        sys.exit(f"eval backend setup: {error}")
