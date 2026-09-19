#!/usr/bin/env python3
"""Package a static linux/amd64 binary as a Docker-loadable scratch image.

No daemon or third-party Python package is required. This creates an image archive;
it is not a substitute for running the image in a Docker/ESXi lab.
"""
import argparse
import hashlib
import io
import json
from pathlib import Path
import tarfile


def add(tar, name, content, mode=0o644):
    info = tarfile.TarInfo(name)
    info.size = len(content)
    info.mode = mode
    info.mtime = 0
    info.uid = info.gid = 0
    info.uname = info.gname = ""
    tar.addfile(info, io.BytesIO(content))


def package(binary_path, archive_path):
    binary = Path(binary_path).read_bytes()
    if binary[:4] != b"\x7fELF" or binary[4:6] != b"\x02\x01" or binary[18:20] != b"\x3e\x00":
        raise ValueError("Expected a little-endian Linux amd64 ELF binary")
    layer = io.BytesIO()
    with tarfile.open(fileobj=layer, mode="w", format=tarfile.USTAR_FORMAT) as tar:
        add(tar, "esxi-mover", binary, 0o755)
    layer_bytes = layer.getvalue()
    layer_hash = hashlib.sha256(layer_bytes).hexdigest()
    config = {
        "created": "1970-01-01T00:00:00Z",
        "architecture": "amd64",
        "os": "linux",
        "config": {
            "User": "65532:65532",
            "ExposedPorts": {"8443/tcp": {}},
            "Entrypoint": ["/esxi-mover"],
        },
        "rootfs": {"type": "layers", "diff_ids": ["sha256:" + layer_hash]},
        "history": [{"created": "1970-01-01T00:00:00Z", "created_by": "ESXi Mover static binary"}],
    }
    config_bytes = json.dumps(config, separators=(",", ":"), sort_keys=True).encode()
    config_hash = hashlib.sha256(config_bytes).hexdigest()
    manifest = [{"Config": config_hash + ".json", "RepoTags": ["esxi-mover:local"], "Layers": [layer_hash + "/layer.tar"]}]
    output = Path(archive_path)
    output.parent.mkdir(parents=True, exist_ok=True)
    with tarfile.open(output, "w", format=tarfile.USTAR_FORMAT) as tar:
        add(tar, config_hash + ".json", config_bytes)
        add(tar, layer_hash + "/layer.tar", layer_bytes)
        add(tar, "manifest.json", json.dumps(manifest, separators=(",", ":")).encode())
    digest = hashlib.sha256(output.read_bytes()).hexdigest()
    output.with_suffix(output.suffix + ".sha256").write_text(digest + "  " + output.name + "\n", encoding="ascii")
    print("Created", output.name, "SHA256", digest)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary")
    parser.add_argument("archive")
    args = parser.parse_args()
    package(args.binary, args.archive)
