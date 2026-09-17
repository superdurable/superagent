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

import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest


MODULE_PATH = Path(__file__).with_name("update_dex_release.py")
SPEC = importlib.util.spec_from_file_location("update_dex_release", MODULE_PATH)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = MODULE
SPEC.loader.exec_module(MODULE)


def manifest() -> dict[str, object]:
    return {
        "release": "1.2.3",
        "sourceCommit": "a" * 40,
        "rolloutOrder": "server-first",
        "runningFlowsCompatibility": "compatible",
        "persistenceCompatibility": "compatible",
        "protocol": {
            "server": {"minimum": 2, "maximum": 3},
            "clients": {"sdkGo": {"minimum": 2, "maximum": 3}},
        },
        "components": {
            "sdkGo": {"version": "1.2.3"},
            "cli": {
                "checksums": {
                    "dexcli_v1.2.3_darwin_amd64.tar.gz": "1" * 64,
                    "dexcli_v1.2.3_darwin_arm64.tar.gz": "2" * 64,
                    "dexcli_v1.2.3_linux_amd64.tar.gz": "3" * 64,
                    "dexcli_v1.2.3_linux_arm64.tar.gz": "4" * 64,
                }
            },
        },
    }


class UpdateDexReleaseTests(unittest.TestCase):
    def test_validates_and_updates_all_superagent_pins(self) -> None:
        value = manifest()
        content = (json.dumps(value) + "\n").encode()
        digest = hashlib.sha256(content).hexdigest()
        url = (
            "https://github.com/superdurable/dex/releases/download/server/v1.2.3/"
            "dex-compatibility-v1.2.3.json"
        )
        validated = MODULE.validate_manifest(url, digest, content)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "script").mkdir()
            (root / "go.mod").write_text(
                "require github.com/superdurable/dex/sdk-go v0.9.0\n", encoding="utf-8"
            )
            (root / "Makefile").write_text("DEXCLI_VERSION := v0.9.0\n", encoding="utf-8")
            (root / "script/install-dexcli.sh").write_text(
                "case x in\n"
                + "\n".join(f"  dexcli_v0.9.0_{name}.tar.gz) checksum=old ;;" for name in (
                    "darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"
                ))
                + "\nesac\n",
                encoding="utf-8",
            )
            MODULE.update_repository(root, url, digest, validated)
            lock = json.loads((root / "dex-release.lock.json").read_text(encoding="utf-8"))
            self.assertEqual(lock["release"], "1.2.3")
            self.assertEqual(lock["protocol"], {"minimum": 2, "maximum": 3})
            self.assertEqual(lock["openFlowsCompatibility"], "cancel-required")
            self.assertIn("sdk-go v1.2.3", (root / "go.mod").read_text(encoding="utf-8"))
            self.assertIn("DEXCLI_VERSION := v1.2.3", (root / "Makefile").read_text(encoding="utf-8"))
            self.assertIn("checksum=" + "4" * 64, (root / "script/install-dexcli.sh").read_text(encoding="utf-8"))

    def test_rejects_tampering_and_incompatible_protocol(self) -> None:
        value = manifest()
        content = (json.dumps(value) + "\n").encode()
        url = (
            "https://github.com/superdurable/dex/releases/download/server/v1.2.3/"
            "dex-compatibility-v1.2.3.json"
        )
        with self.assertRaisesRegex(MODULE.UpgradeError, "SHA-256 mismatch"):
            MODULE.validate_manifest(url, "0" * 64, content)
        incompatible = copy.deepcopy(value)
        incompatible["protocol"]["clients"]["sdkGo"] = {"minimum": 4, "maximum": 4}
        incompatible_content = (json.dumps(incompatible) + "\n").encode()
        with self.assertRaisesRegex(MODULE.UpgradeError, "protocols are incompatible"):
            MODULE.validate_manifest(
                url,
                hashlib.sha256(incompatible_content).hexdigest(),
                incompatible_content,
            )


if __name__ == "__main__":
    unittest.main()
