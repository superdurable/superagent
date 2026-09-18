#!/usr/bin/env python3
# Copyright (c) 2026 Super Durable, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
# SPDX-License-Identifier: Apache-2.0

"""Prepare a reviewed SuperAgent upgrade from one immutable Dex manifest."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import re
import urllib.request
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
SEMVER = re.compile(r"[0-9]+\.[0-9]+\.[0-9]+")
SHA256 = re.compile(r"[0-9a-f]{64}")
DEX_MANIFEST_URL = re.compile(
    r"https://github\.com/superdurable/dex/releases/download/server/v"
    r"([0-9]+\.[0-9]+\.[0-9]+)/dex-compatibility-v\1\.json"
)


class UpgradeError(RuntimeError):
    """The requested Dex upgrade is incomplete or inconsistent."""


def require(condition: bool, message: str) -> None:
    if not condition:
        raise UpgradeError(message)


def download(url: str) -> bytes:
    request = urllib.request.Request(url, headers={"User-Agent": "superagent-dex-upgrade/1"})
    with urllib.request.urlopen(request, timeout=30) as response:
        return response.read()


def replace_once(path: Path, pattern: str, replacement: str) -> None:
    content = path.read_text(encoding="utf-8")
    updated, count = re.subn(pattern, replacement, content, count=1, flags=re.MULTILINE)
    require(count == 1, f"expected one version pin in {path}")
    path.write_text(updated, encoding="utf-8")


def validate_manifest(
    manifest_url: str,
    manifest_sha256: str,
    content: bytes,
) -> dict[str, Any]:
    match = DEX_MANIFEST_URL.fullmatch(manifest_url)
    require(match is not None, "manifest URL is not an immutable Dex Server release asset")
    require(SHA256.fullmatch(manifest_sha256) is not None, "manifest SHA-256 is invalid")
    require(hashlib.sha256(content).hexdigest() == manifest_sha256, "manifest SHA-256 mismatch")
    manifest = json.loads(content)
    version = match.group(1)
    require(manifest["release"] == version, "manifest release does not match its URL")
    require(manifest["rolloutOrder"] == "server-first", "Dex rollout must be server-first")
    require(
        manifest["components"]["sdkGo"]["version"] == version,
        "Dex Go SDK version does not match the release",
    )
    server_protocol = manifest["protocol"]["server"]
    go_protocol = manifest["protocol"]["clients"]["sdkGo"]
    require(
        max(server_protocol["minimum"], go_protocol["minimum"])
        <= min(server_protocol["maximum"], go_protocol["maximum"]),
        "Dex Go SDK and Server protocols are incompatible",
    )
    require(manifest["persistenceCompatibility"] == "compatible", "Dex persistence is incompatible")
    return manifest


def update_repository(
    root: Path,
    manifest_url: str,
    manifest_sha256: str,
    manifest: dict[str, Any],
    *,
    server_only: bool = False,
) -> None:
    version = manifest["release"]
    previous = None
    if server_only:
        previous = json.loads((root / "dex-release.lock.json").read_text(encoding="utf-8"))
        server_protocol = manifest["protocol"]["server"]
        sdk_protocol = previous["protocol"]
        require(
            max(server_protocol["minimum"], sdk_protocol["minimum"])
            <= min(server_protocol["maximum"], sdk_protocol["maximum"]),
            "retained Go SDK and new Server protocols are incompatible",
        )
    checksums = manifest["components"]["cli"]["checksums"]
    archives = tuple(
        f"dexcli_v{version}_{platform}_{architecture}.tar.gz"
        for platform in ("darwin", "linux")
        for architecture in ("amd64", "arm64")
    )
    require(set(checksums) == set(archives), "Dex CLI checksums are incomplete")
    if not server_only:
        replace_once(
            root / "go.mod",
            r"(github\.com/superdurable/dex/sdk-go\s+)v[^\s]+",
            rf"\g<1>v{version}",
        )
    replace_once(root / "Makefile", r"^DEXCLI_VERSION := v[^\s]+$", f"DEXCLI_VERSION := v{version}")
    installer = root / "script/install-dexcli.sh"
    installer_content = installer.read_text(encoding="utf-8")
    cases = "\n".join(
        f"  {archive}) checksum={checksums[archive]} ;;"
        for archive in archives
    )
    updated, count = re.subn(
        r"  dexcli_v[^\n]+\n  dexcli_v[^\n]+\n  dexcli_v[^\n]+\n  dexcli_v[^\n]+",
        cases,
        installer_content,
        count=1,
    )
    require(count == 1, "expected four Dex CLI checksum pins")
    installer.write_text(updated, encoding="utf-8")

    lock = {
        "schemaVersion": 1,
        "release": version,
        "manifest": {"url": manifest_url, "sha256": manifest_sha256},
        "sourceCommit": manifest["sourceCommit"],
        "sdkGoVersion": manifest["components"]["sdkGo"]["version"],
        "protocol": manifest["protocol"]["clients"]["sdkGo"],
        "runningFlowsCompatibility": manifest["runningFlowsCompatibility"],
        "persistenceCompatibility": manifest["persistenceCompatibility"],
        "openFlowsCompatibility": "cancel-required",
    }
    if previous is not None:
        lock["sdkGoVersion"] = previous["sdkGoVersion"]
        lock["protocol"] = previous["protocol"]
        lock["sdkManifest"] = previous.get("sdkManifest", previous["manifest"])
    (root / "dex-release.lock.json").write_text(
        json.dumps(lock, indent=2) + "\n",
        encoding="utf-8",
    )


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--manifest-url", required=True)
    parser.add_argument("--manifest-sha256", required=True)
    parser.add_argument("--server-only", action="store_true", help="Retain the locked Go SDK and verify protocol overlap")
    args = parser.parse_args()
    content = download(args.manifest_url)
    manifest = validate_manifest(args.manifest_url, args.manifest_sha256, content)
    update_repository(ROOT, args.manifest_url, args.manifest_sha256, manifest, server_only=args.server_only)
    print(f'Prepared SuperAgent for Dex {manifest["release"]}; open Flows require review')


if __name__ == "__main__":
    main()
