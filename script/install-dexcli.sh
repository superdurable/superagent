#!/bin/sh
# Copyright (c) 2022-2026 Super Durable, Inc.
# Licensed under the Apache License, Version 2.0.
# SPDX-License-Identifier: Apache-2.0

set -eu

destination=${1:?destination path is required}
version=${2:?dexcli version is required}

case "$(uname -s)" in
  Darwin) operating_system=darwin ;;
  Linux) operating_system=linux ;;
  *) echo "unsupported operating system: $(uname -s)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  arm64|aarch64) architecture=arm64 ;;
  x86_64|amd64) architecture=amd64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

archive_name="dexcli_${version}_${operating_system}_${architecture}.tar.gz"
case "$archive_name" in
  dexcli_v0.4.0_darwin_amd64.tar.gz) checksum=9aaa83bec16c2512193116ac0d6d5d887f16c1c8c19ae169b9457e910c3ac54b ;;
  dexcli_v0.4.0_darwin_arm64.tar.gz) checksum=cf6202f30dc6d85bd1109383b0cbc5083faf8b7ac03b28dd8d134fd698450e73 ;;
  dexcli_v0.4.0_linux_amd64.tar.gz) checksum=c5f2db8786b4a75d77dceed4513f0b3a48c43f0993e8d2e2060c0754571e5d5d ;;
  dexcli_v0.4.0_linux_arm64.tar.gz) checksum=62655f6b34c1b829343b59d2fce152a37cc15e2506c21eea8d06f457c98fb359 ;;
  *) echo "no checksum is pinned for $archive_name" >&2; exit 1 ;;
esac

temporary_directory=$(mktemp -d "${TMPDIR:-/tmp}/superagent-dexcli.XXXXXX")
trap 'rm -r "$temporary_directory"' EXIT HUP INT TERM
archive="$temporary_directory/$archive_name"
url="https://github.com/superdurable/dex/releases/download/cli-$version/$archive_name"

curl --fail --location --proto '=https' --retry 3 --silent --show-error \
  --tlsv1.2 --output "$archive" "$url"
if command -v sha256sum >/dev/null 2>&1; then
  actual_checksum=$(sha256sum "$archive" | awk '{print $1}')
else
  actual_checksum=$(shasum -a 256 "$archive" | awk '{print $1}')
fi
if [ "$actual_checksum" != "$checksum" ]; then
  echo "dexcli checksum mismatch" >&2
  exit 1
fi

tar -xzf "$archive" -C "$temporary_directory"
mkdir -p "$(dirname "$destination")"
install -m 0755 "$temporary_directory/dexcli" "$destination"
