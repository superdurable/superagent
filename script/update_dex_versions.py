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

"""Update SuperAgent's independent Dex Go SDK and dexcli pins."""

from __future__ import annotations

import argparse
from pathlib import Path
import re
import urllib.request


ROOT = Path(__file__).resolve().parents[1]
SEMVER = re.compile(r"[0-9]+\.[0-9]+\.[0-9]+")
PLATFORMS = ("darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64")


class DexVersionError(RuntimeError):
    """A requested Dex component version or release asset is invalid."""


def require(condition: bool, message: str) -> None:
    if not condition:
        raise DexVersionError(message)


def replace_once(path: Path, pattern: str, replacement: str) -> None:
    content = path.read_text(encoding="utf-8")
    updated, count = re.subn(pattern, replacement, content, count=1, flags=re.MULTILINE)
    require(count == 1, f"expected one version pin in {path}")
    path.write_text(updated, encoding="utf-8")


def download_cli_checksums(version: str) -> bytes:
    url = f"https://github.com/superdurable/dex/releases/download/cli-v{version}/checksums.txt"
    request = urllib.request.Request(url, headers={"User-Agent": "superagent-dexcli-upgrade/1"})
    with urllib.request.urlopen(request, timeout=30) as response:
        return response.read()


def parse_cli_checksums(version: str, content: bytes) -> dict[str, str]:
    checksums: dict[str, str] = {}
    for line in content.decode("utf-8").splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})\s+\*?\.?/?(dexcli_v[^/\s]+\.tar\.gz)", line)
        if match is not None:
            checksums[match.group(2)] = match.group(1)
    expected = {f"dexcli_v{version}_{platform}.tar.gz" for platform in PLATFORMS}
    require(set(checksums) == expected, "dexcli checksums.txt does not contain the four release archives")
    return checksums


def update_repository(
    root: Path,
    sdk_go_version: str,
    dexcli_version: str,
    cli_checksums: dict[str, str],
) -> None:
    require(SEMVER.fullmatch(sdk_go_version) is not None, "invalid Dex Go SDK version")
    require(SEMVER.fullmatch(dexcli_version) is not None, "invalid dexcli version")
    replace_once(
        root / "go.mod",
        r"(github\.com/superdurable/dex/sdk-go\s+)v[^\s]+",
        rf"\g<1>v{sdk_go_version}",
    )
    replace_once(
        root / "Makefile", r"^DEXCLI_VERSION := v[^\s]+$", f"DEXCLI_VERSION := v{dexcli_version}"
    )
    installer = root / "script/install-dexcli.sh"
    cases = "\n".join(
        f"  {archive}) checksum={cli_checksums[archive]} ;;"
        for archive in sorted(cli_checksums)
    )
    content = installer.read_text(encoding="utf-8")
    updated, count = re.subn(
        r"  dexcli_v[^\n]+\n  dexcli_v[^\n]+\n  dexcli_v[^\n]+\n  dexcli_v[^\n]+",
        cases,
        content,
        count=1,
    )
    require(count == 1, "expected four dexcli checksum pins")
    installer.write_text(updated, encoding="utf-8")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--sdk-go-version", required=True)
    parser.add_argument("--dexcli-version", required=True)
    parser.add_argument("--checksums-file", type=Path)
    arguments = parser.parse_args()
    checksum_content = (
        arguments.checksums_file.read_bytes()
        if arguments.checksums_file is not None
        else download_cli_checksums(arguments.dexcli_version)
    )
    checksums = parse_cli_checksums(arguments.dexcli_version, checksum_content)
    update_repository(ROOT, arguments.sdk_go_version, arguments.dexcli_version, checksums)
    print(
        f"Updated Dex Go SDK to {arguments.sdk_go_version} and dexcli to {arguments.dexcli_version}"
    )


if __name__ == "__main__":
    main()
