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

"""Verify SuperAgent's direct Dex dependency against its immutable release lock."""

from __future__ import annotations

import json
from pathlib import Path
import re

import update_dex_release


ROOT = Path(__file__).resolve().parents[1]


def main() -> None:
    lock = json.loads((ROOT / "dex-release.lock.json").read_text(encoding="utf-8"))
    if set(lock) - {"sdkManifest", "sdkGoRelease"} != {
        "schemaVersion",
        "release",
        "manifest",
        "sourceCommit",
        "sdkGoVersion",
        "protocol",
        "runningFlowsCompatibility",
        "persistenceCompatibility",
        "openFlowsCompatibility",
    }:
        raise update_dex_release.UpgradeError("Dex release lock has unexpected fields")
    update_dex_release.require(
        not ({"sdkManifest", "sdkGoRelease"} <= set(lock)),
        "Dex release lock cannot use two SDK sources",
    )
    content = update_dex_release.download(lock["manifest"]["url"])
    manifest = update_dex_release.validate_manifest(
        lock["manifest"]["url"], lock["manifest"]["sha256"], content
    )
    sdk_manifest = manifest
    if "sdkManifest" in lock:
        source = lock["sdkManifest"]
        sdk_manifest = update_dex_release.validate_manifest(
            source["url"], source["sha256"], update_dex_release.download(source["url"])
        )
    sdk_release = lock.get("sdkGoRelease")
    if sdk_release is not None:
        update_dex_release.require(
            set(sdk_release) == {"tag", "sourceCommit", "moduleChecksum", "goModChecksum"},
            "Dex Go SDK release lock has unexpected fields",
        )
        version = lock["sdkGoVersion"]
        update_dex_release.require(
            sdk_release["tag"] == f"sdk-go/v{version}",
            "Dex Go SDK tag mismatch",
        )
        update_dex_release.require(
            re.fullmatch(r"[0-9a-f]{40}", sdk_release["sourceCommit"]) is not None,
            "Dex Go SDK source commit is invalid",
        )
        update_dex_release.require(
            re.fullmatch(r"h1:[A-Za-z0-9+/]+={0,2}", sdk_release["moduleChecksum"])
            is not None,
            "Dex Go SDK module checksum is invalid",
        )
        update_dex_release.require(
            re.fullmatch(r"h1:[A-Za-z0-9+/]+={0,2}", sdk_release["goModChecksum"])
            is not None,
            "Dex Go SDK go.mod checksum is invalid",
        )
    requirements = dict(
        re.findall(r"(?m)^\s*([^\s()]+)\s+(v[^\s]+)(?:\s+//.*)?$", (ROOT / "go.mod").read_text(encoding="utf-8"))
    )
    update_dex_release.require(lock["schemaVersion"] == 1, "unsupported Dex release lock")
    update_dex_release.require(lock["release"] == manifest["release"], "Dex release mismatch")
    update_dex_release.require(
        lock["sourceCommit"] == manifest["sourceCommit"], "Dex source commit mismatch"
    )
    if sdk_release is None:
        update_dex_release.require(
            lock["sdkGoVersion"] == sdk_manifest["components"]["sdkGo"]["version"],
            "Dex Go SDK version mismatch",
        )
    update_dex_release.require(
        requirements.get("github.com/superdurable/dex/sdk-go") == f'v{lock["sdkGoVersion"]}',
        "SuperAgent must directly require the locked Dex Go SDK",
    )
    if sdk_release is None:
        update_dex_release.require(
            lock["protocol"] == sdk_manifest["protocol"]["clients"]["sdkGo"],
            "Dex protocol mismatch",
        )
    else:
        sums = (ROOT / "go.sum").read_text(encoding="utf-8").splitlines()
        module = f'github.com/superdurable/dex/sdk-go v{lock["sdkGoVersion"]}'
        update_dex_release.require(
            f'{module} {sdk_release["moduleChecksum"]}' in sums,
            "Dex Go SDK module checksum mismatch",
        )
        update_dex_release.require(
            f'{module}/go.mod {sdk_release["goModChecksum"]}' in sums,
            "Dex Go SDK go.mod checksum mismatch",
        )
    server_protocol = manifest["protocol"]["server"]
    update_dex_release.require(
        max(lock["protocol"]["minimum"], server_protocol["minimum"])
        <= min(lock["protocol"]["maximum"], server_protocol["maximum"]),
        "locked Go SDK and Server protocols are incompatible",
    )
    for field in ("runningFlowsCompatibility", "persistenceCompatibility"):
        update_dex_release.require(lock[field] == manifest[field], f"Dex {field} mismatch")
    update_dex_release.require(
        lock["openFlowsCompatibility"] in {"compatible", "cancel-required"},
        "invalid open Flow compatibility",
    )
    print(f'SuperAgent locks Dex Server {lock["release"]} and Go SDK {lock["sdkGoVersion"]}')


if __name__ == "__main__":
    main()
