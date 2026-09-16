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
  dexcli_v0.9.0_darwin_amd64.tar.gz) checksum=071f530422e869554b2e2a2dc10ce5d917e1093a38a5af4e1438192e9c532408 ;;
  dexcli_v0.9.0_darwin_arm64.tar.gz) checksum=4ee2df39d0218169b5fe0fc581e9cac2c1f40e24a011ac5c5ba441eccdfd1f51 ;;
  dexcli_v0.9.0_linux_amd64.tar.gz) checksum=0df459cdde367191e7c962b819a1491073b614da93f5459129f38f90970a7016 ;;
  dexcli_v0.9.0_linux_arm64.tar.gz) checksum=68f5771cde6ae4a1cfb8c78efb35881765273d4727d6353de41d6b4252476d67 ;;
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
