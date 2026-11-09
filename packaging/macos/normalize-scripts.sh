#!/bin/bash
# pkgbuild --ownership applies to Payload, but not its Scripts archive. Encode
# root ownership without an elevated build so Installer never sources code
# owned by the builder account. This modifies an unsigned component only.
normalize_scripts_archive() (
  set -euo pipefail
  umask 077
  local package="$1" scripts="$2" scratch
  scratch="$(mktemp -d "${TMPDIR:-/tmp}/tenebra-script-archive.XXXXXX")"
  trap '/bin/rm -rf "$scratch"' EXIT
  mkdir "$scratch/raw"
  /usr/bin/xar -xf "$package" -C "$scratch/raw"
  COPYFILE_DISABLE=1 /usr/bin/tar --format cpio --uid 0 --gid 0 --uname root --gname wheel \
    --no-xattrs --no-mac-metadata -czf "$scratch/raw/Scripts" -C "$scripts" .
  (
    cd "$scratch/raw"
    /usr/bin/xar --compression none -cf "$scratch/normalized.pkg" Bom PackageInfo Payload Scripts
  )
  /bin/mv "$scratch/normalized.pkg" "$package"
)
