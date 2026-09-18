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

"""Verify SuperAgent's direct Dex Go SDK and dexcli version pins."""

from __future__ import annotations

from pathlib import Path
import re


ROOT = Path(__file__).resolve().parents[1]
SEMVER = r"[0-9]+\.[0-9]+\.[0-9]+"


class DexVersionError(RuntimeError):
    """A Dex component version pin is missing or inconsistent."""


def require_match(pattern: str, content: str, label: str) -> re.Match[str]:
    match = re.search(pattern, content, flags=re.MULTILINE)
    if match is None:
        raise DexVersionError(f"{label} version pin is missing")
    return match


def read_versions(root: Path) -> tuple[str, str]:
    go_mod = (root / "go.mod").read_text(encoding="utf-8")
    sdk_version = require_match(
        rf"^\s*github\.com/superdurable/dex/sdk-go\s+v({SEMVER})\s*$",
        go_mod,
        "Dex Go SDK",
    ).group(1)

    makefile = (root / "Makefile").read_text(encoding="utf-8")
    dexcli_version = require_match(
        rf"^DEXCLI_VERSION := v({SEMVER})$", makefile, "dexcli Makefile"
    ).group(1)

    installer = (root / "script/install-dexcli.sh").read_text(encoding="utf-8")
    archives = re.findall(
        r"dexcli_v([0-9]+\.[0-9]+\.[0-9]+)_(?:darwin|linux)_(?:amd64|arm64)\.tar\.gz",
        installer,
    )
    if len(archives) != 4 or set(archives) != {dexcli_version}:
        raise DexVersionError("dexcli installer versions do not match the Makefile")

    workflow = (root / ".github/workflows/ci.yml").read_text(encoding="utf-8")
    expected_binary = f".cache/dexcli-v{dexcli_version} dev"
    if expected_binary not in workflow:
        raise DexVersionError("dexcli CI version does not match the Makefile")
    return sdk_version, dexcli_version


def main() -> None:
    sdk_version, dexcli_version = read_versions(ROOT)
    print(f"SuperAgent uses Dex Go SDK {sdk_version} and dexcli {dexcli_version}")


if __name__ == "__main__":
    main()
