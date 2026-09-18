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

from __future__ import annotations

import importlib.util
from pathlib import Path
import sys
import tempfile
import unittest


def load_module(name: str):
    path = Path(__file__).with_name(f"{name}.py")
    spec = importlib.util.spec_from_file_location(name, path)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


CHECK = load_module("check_dex_versions")
UPDATE = load_module("update_dex_versions")


def checksums(version: str) -> bytes:
    return "\n".join(
        f"{'1234abcd' * 8}  ./dexcli_v{version}_{platform}.tar.gz"
        for platform in UPDATE.PLATFORMS
    ).encode()


class DexVersionTests(unittest.TestCase):
    def test_updates_and_checks_independent_component_versions(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "script").mkdir()
            (root / ".github/workflows").mkdir(parents=True)
            (root / "go.mod").write_text(
                "require (\n\tgithub.com/superdurable/dex/sdk-go v0.9.0\n)\n", encoding="utf-8"
            )
            (root / "Makefile").write_text("DEXCLI_VERSION := v0.9.0\n", encoding="utf-8")
            (root / "script/install-dexcli.sh").write_text(
                "\n".join(
                    f"  dexcli_v0.9.0_{platform}.tar.gz) checksum=old ;;"
                    for platform in UPDATE.PLATFORMS
                )
                + "\n",
                encoding="utf-8",
            )
            (root / ".github/workflows/ci.yml").write_text(
                ".cache/dexcli-v1.2.4 dev\n", encoding="utf-8"
            )
            parsed = UPDATE.parse_cli_checksums("1.2.4", checksums("1.2.4"))
            UPDATE.update_repository(root, "1.2.3", "1.2.4", parsed)
            self.assertEqual(CHECK.read_versions(root), ("1.2.3", "1.2.4"))

    def test_rejects_incomplete_checksums_and_version_drift(self) -> None:
        with self.assertRaisesRegex(UPDATE.DexVersionError, "four release archives"):
            UPDATE.parse_cli_checksums("1.2.3", b"")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "script").mkdir()
            (root / ".github/workflows").mkdir(parents=True)
            (root / "go.mod").write_text(
                "require (\n\tgithub.com/superdurable/dex/sdk-go v1.2.3\n)\n", encoding="utf-8"
            )
            (root / "Makefile").write_text("DEXCLI_VERSION := v1.2.3\n", encoding="utf-8")
            (root / "script/install-dexcli.sh").write_text(
                "\n".join(
                    f"  dexcli_v1.2.2_{platform}.tar.gz) checksum=old ;;"
                    for platform in UPDATE.PLATFORMS
                ),
                encoding="utf-8",
            )
            (root / ".github/workflows/ci.yml").write_text(
                ".cache/dexcli-v1.2.3 dev\n", encoding="utf-8"
            )
            with self.assertRaisesRegex(CHECK.DexVersionError, "installer versions"):
                CHECK.read_versions(root)


if __name__ == "__main__":
    unittest.main()
