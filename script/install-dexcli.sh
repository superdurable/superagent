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
  dexcli_v0.8.0_darwin_amd64.tar.gz) checksum=56a690eba38fb7cfb626113c6149326104cd7952cfbd0cc061c1d0832302c148 ;;
  dexcli_v0.8.0_darwin_arm64.tar.gz) checksum=afa61346c6d7d0aa3b2ce16077cad4840db06f33ae6a42a947e44a3afee9203f ;;
  dexcli_v0.8.0_linux_amd64.tar.gz) checksum=71541bb0686364a8b542660e9925add808891ce778a8fe7c2f0d6d5d12f735dd ;;
  dexcli_v0.8.0_linux_arm64.tar.gz) checksum=64da8b6ff743323e0916306bb8e49123c1cb0be3bfa0af86a674f750f910dc30 ;;
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
