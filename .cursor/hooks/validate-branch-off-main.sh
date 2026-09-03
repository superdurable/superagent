#!/bin/sh
# Copyright (c) 2026 Super Durable, Inc.
# Licensed under the Apache License, Version 2.0.
# SPDX-License-Identifier: Apache-2.0

root=$(git rev-parse --show-toplevel 2>/dev/null) || exit 0
exec python3 "$root/script/branch_off_main_policy.py" cursor
