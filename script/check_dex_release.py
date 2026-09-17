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
    if set(lock) != {
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
    content = update_dex_release.download(lock["manifest"]["url"])
    manifest = update_dex_release.validate_manifest(
        lock["manifest"]["url"], lock["manifest"]["sha256"], content
    )
    requirements = dict(
        re.findall(r"(?m)^\s*([^\s()]+)\s+(v[^\s]+)(?:\s+//.*)?$", (ROOT / "go.mod").read_text(encoding="utf-8"))
    )
    update_dex_release.require(lock["schemaVersion"] == 1, "unsupported Dex release lock")
    update_dex_release.require(lock["release"] == manifest["release"], "Dex release mismatch")
    update_dex_release.require(
        lock["sourceCommit"] == manifest["sourceCommit"], "Dex source commit mismatch"
    )
    update_dex_release.require(
        lock["sdkGoVersion"] == manifest["components"]["sdkGo"]["version"],
        "Dex Go SDK version mismatch",
    )
    update_dex_release.require(
        requirements.get("github.com/superdurable/dex/sdk-go") == f'v{lock["sdkGoVersion"]}',
        "SuperAgent must directly require the locked Dex Go SDK",
    )
    update_dex_release.require(
        lock["protocol"] == manifest["protocol"]["clients"]["sdkGo"],
        "Dex protocol mismatch",
    )
    for field in ("runningFlowsCompatibility", "persistenceCompatibility"):
        update_dex_release.require(lock[field] == manifest[field], f"Dex {field} mismatch")
    update_dex_release.require(
        lock["openFlowsCompatibility"] in {"compatible", "cancel-required"},
        "invalid open Flow compatibility",
    )
    print(f'SuperAgent directly requires locked Dex {lock["release"]}')


if __name__ == "__main__":
    main()
