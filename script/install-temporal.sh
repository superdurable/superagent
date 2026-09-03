#!/bin/sh
# Copyright (c) 2022-2026 Super Durable, Inc.
# Licensed under the Apache License, Version 2.0.
# SPDX-License-Identifier: Apache-2.0

set -eu

destination=${1:?destination path is required}
version=${2:?Temporal CLI version is required}
release_version=${version#v}

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

archive_name="temporal_cli_${release_version}_${operating_system}_${architecture}.tar.gz"
case "$archive_name" in
  temporal_cli_1.8.2_darwin_amd64.tar.gz) checksum=489d7f5420cae02b559774ac23df035141954c33a51dba96f5759a0ddccdf1b6 ;;
  temporal_cli_1.8.2_darwin_arm64.tar.gz) checksum=dacdc3587682c04cf27e67c8878ca2d755230b6ad63c0c6ebddd7348ae90ed94 ;;
  temporal_cli_1.8.2_linux_amd64.tar.gz) checksum=d8421bda989e6514b4bdb4d63a9012a8a05a806892e881a5aad8510496349a94 ;;
  temporal_cli_1.8.2_linux_arm64.tar.gz) checksum=83600a8fac6e3da54093e5da6918d399f501532b9f1172235603f9606f4ac6e4 ;;
  *) echo "no checksum is pinned for $archive_name" >&2; exit 1 ;;
esac

temporary_directory=$(mktemp -d "${TMPDIR:-/tmp}/superagent-temporal.XXXXXX")
trap 'rm -r "$temporary_directory"' EXIT HUP INT TERM
archive="$temporary_directory/$archive_name"
url="https://github.com/temporalio/cli/releases/download/$version/$archive_name"

curl --fail --location --proto '=https' --retry 3 --silent --show-error \
  --tlsv1.2 --output "$archive" "$url"
if command -v sha256sum >/dev/null 2>&1; then
  actual_checksum=$(sha256sum "$archive" | awk '{print $1}')
else
  actual_checksum=$(shasum -a 256 "$archive" | awk '{print $1}')
fi
if [ "$actual_checksum" != "$checksum" ]; then
  echo "Temporal CLI checksum mismatch" >&2
  exit 1
fi

tar -xzf "$archive" -C "$temporary_directory"
mkdir -p "$(dirname "$destination")"
install -m 0755 "$temporary_directory/temporal" "$destination"
