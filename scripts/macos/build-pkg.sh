#!/bin/bash
# Build an unsigned local Installer package. The app and helpers are ad-hoc
# signed; this script never reads Developer ID identities or signing keys.

die() { echo "build-pkg: $*" >&2; exit 1; }
usage() { echo 'Usage: build-pkg.sh --app /absolute/Tenebra.app --output /absolute/Tenebra.pkg --go /absolute/go [--arch arm64|universal]'; }

read_source_metadata() {
  local pin
  VERSION="$(sed -n 's/^const Version = "\([^"]*\)"$/\1/p' "$REPO_ROOT/core/buildinfo/buildinfo.go")"
  [[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die 'cannot read pinned core version'
  pin="$(cat "$REPO_ROOT/.go-version")"
  [[ "$pin" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die 'invalid pinned compiler in .go-version'
  EXPECTED_GO="go$pin"
}

configure_architecture() {
  case "$1" in
    arm64) REQUIRED_ARCHES=(arm64); HOST_ARCHITECTURES=arm64 ;;
    universal) REQUIRED_ARCHES=(arm64 x86_64); HOST_ARCHITECTURES=arm64,x86_64 ;;
    *) die '--arch must be arm64 or universal' ;;
  esac
}

validate_go_tool() {
  local actual
  export GOTOOLCHAIN=local GOWORK=off GOMAXPROCS=4 GOFLAGS=-p=4
  [[ "$GO_TOOL" == /* && -x "$GO_TOOL" && -f "$GO_TOOL" ]] || die 'use --go with an absolute executable compiler path'
  actual="$("$GO_TOOL" version)" || die 'cannot read selected Go compiler version'
  case "$actual" in
    "go version $EXPECTED_GO darwin/arm64"|"go version $EXPECTED_GO darwin/amd64") : ;;
    *) die "Go $EXPECTED_GO darwin/arm64 or darwin/amd64 from .go-version is required; use --go" ;;
  esac
}

validate_macho() {
  local path="$1" arches arch build minos platform signature
  [[ -f "$path" && -s "$path" && -x "$path" && ! -L "$path" ]] || die "missing executable payload: $path"
  arches="$(lipo -archs "$path")" || die "cannot read Mach-O architectures: $path"
  for arch in "${REQUIRED_ARCHES[@]}"; do
    case " $arches " in *" $arch "*) : ;; *) die "executable is missing $arch: $path" ;; esac
    build="$(xcrun vtool -arch "$arch" -show-build "$path")" || die "cannot read $arch macOS load commands: $path"
    platform="$(printf '%s\n' "$build" | awk '/^[[:space:]]*platform / { print $2 }')"
    minos="$(printf '%s\n' "$build" | awk '/^[[:space:]]*minos / { print $2 }')"
    [[ "$platform" == MACOS ]] || die "expected macOS platform for $arch: $path"
    [[ "$minos" =~ ^[0-9]+\.[0-9]+(\.[0-9]+)?$ ]] || die "cannot read $arch executable macOS minimum: $path"
    # Compatible lower load-command minima are accepted, but every requested
    # slice must run on the Installer's deliberately fixed macOS 15.0 floor.
    printf '%s\n' "$minos" | awk -F. '{ if ($1>15 || ($1==15 && ($2>0 || $3>0))) exit 1 }' || die "$arch executable requires macOS later than declared 15.0 floor: $path"
    signature="$(codesign -dv --architecture "$arch" "$path" 2>&1)" || die "cannot read $arch executable signature: $path"
    [[ "$(printf '%s\n' "$signature" | awk -F= '/^Signature=/ { print $2 }')" == adhoc ]] || die "expected an ad-hoc $arch executable signature: $path"
  done
  codesign --verify --strict --all-architectures "$path" || die "executable signature verification failed: $path"
}

validate_core_metadata() {
  local core="$1" scratch="$2" arches arch goarch thin metadata
  arches="$(lipo -archs "$core")" || die 'cannot read core architectures'
  for arch in "${REQUIRED_ARCHES[@]}"; do
    case " $arches " in *" $arch "*) : ;; *) die "core is missing $arch" ;; esac
    thin="$scratch/core-metadata-$arch"
    if [[ "$arches" == "$arch" ]]; then
      # lipo -thin rejects an already-thin input; inspect a private copy.
      cp "$core" "$thin"
    else
      lipo "$core" -thin "$arch" -output "$thin" || die "cannot extract $arch core metadata"
    fi
    chmod 600 "$thin"
    metadata="$("$GO_TOOL" version -m "$thin")" || die "cannot read $arch core metadata"
    [[ "${metadata%%$'\n'*}" == "$thin: $EXPECTED_GO" ]] || die "unexpected $arch core toolchain; expected $EXPECTED_GO"
    [[ "$(printf '%s\n' "$metadata" | awk -F '\t' '$2=="path" { print $3 }')" == github.com/Divaaaan/tenebra/cmd/tenebra-core ]] || die "unexpected $arch core module path"
    goarch="$arch"; [[ "$arch" != x86_64 ]] || goarch=amd64
    [[ "$(printf '%s\n' "$metadata" | awk -F '\t' '$2=="build" && $3~/^GOOS=/ { print $3 }')" == GOOS=darwin &&
       "$(printf '%s\n' "$metadata" | awk -F '\t' '$2=="build" && $3~/^GOARCH=/ { print $3 }')" == "GOARCH=$goarch" ]] || die "unexpected $arch core target"
    rm "$thin"
  done
}

build_healthcheck() {
  local output="$1" scratch="$2" arch goarch
  local slices=()
  for arch in "${REQUIRED_ARCHES[@]}"; do
    goarch="$arch"; [[ "$arch" != x86_64 ]] || goarch=amd64
    (
      cd "$REPO_ROOT"
      GOTOOLCHAIN=local GOWORK=off GOOS=darwin GOARCH="$goarch" CGO_ENABLED=0 "$GO_TOOL" build \
        -trimpath -buildvcs=true -o "$scratch/checker-$arch" ./cmd/tenebra-healthcheck
    )
    slices+=("$scratch/checker-$arch")
  done
  if [[ "${#slices[@]}" -eq 1 ]]; then
    cp "${slices[0]}" "$output"
  else
    lipo -create "${slices[@]}" -output "$output"
  fi
  chmod 755 "$output"
  codesign --force --sign - --timestamp=none "$output"
  validate_macho "$output"
}

render_distribution() {
  sed -e "s/@VERSION@/$VERSION/g" -e "s/@HOST_ARCHITECTURES@/$HOST_ARCHITECTURES/g" "$1" > "$2"
}

main() {
  set -euo pipefail
  # Resolve the optional local default before narrowing the command search path.
  GO_TOOL="$(command -v "${TENEBRA_GO:-go}" || true)"
  PATH=/usr/bin:/bin:/usr/sbin:/sbin
  export PATH
  umask 077
  REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
  APP=''
  OUTPUT=''
  ARCH=arm64
  STAGE=''
  while [[ "$#" -gt 0 ]]; do
    case "$1" in
      --app) [[ "$#" -ge 2 ]] || die '--app requires a path'; APP="$2"; shift 2 ;;
      --output) [[ "$#" -ge 2 ]] || die '--output requires a path'; OUTPUT="$2"; shift 2 ;;
      --go) [[ "$#" -ge 2 ]] || die '--go requires a toolchain'; GO_TOOL="$2"; shift 2 ;;
      --arch) [[ "$#" -ge 2 ]] || die '--arch requires arm64 or universal'; ARCH="$2"; shift 2 ;;
      -h|--help) usage; exit 0 ;;
      *) die "unknown argument: $1" ;;
    esac
  done
  configure_architecture "$ARCH"
  [[ "$APP" == /*.app && -d "$APP" && ! -L "$APP" ]] || die '--app must be an absolute regular .app bundle'
  [[ "$OUTPUT" == /*.pkg ]] || die '--output must be an absolute .pkg path'
  [[ ! -e "$OUTPUT" && ! -L "$OUTPUT" ]] || die 'output already exists'
  [[ -d "$(dirname "$OUTPUT")" ]] || die 'output parent does not exist'
  [[ "$(uname -s)" == Darwin ]] || die 'macOS is required'
  [[ -z "$(find "$APP" ! -type d ! -type f -print -quit)" ]] || die 'bundle contains a symbolic link or special file'

  INFO="$APP/Contents/Info.plist"
  read_source_metadata
  [[ "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$INFO")" == "$VERSION" &&
     "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleVersion' "$INFO")" == "$VERSION" ]] || die 'bundle version does not match core'
  [[ "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$INFO")" == com.tenebra.desktop ]] || die 'unexpected bundle identifier'
  MAIN="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$INFO")"
  [[ "$MAIN" != */* && "$MAIN" != .* && -n "$MAIN" ]] || die 'invalid bundle executable name'
  CORE="$APP/Contents/MacOS/tenebra-core"
  RESOURCES="$APP/Contents/Resources/resources"
  for path in "$APP/Contents/MacOS/$MAIN" "$CORE" "$RESOURCES/sing-box" "$RESOURCES/geoip-ru.srs" "$RESOURCES/geosite-ru.srs" "$RESOURCES/geosite-ads.srs"; do
    [[ -f "$path" && -s "$path" && ! -L "$path" ]] || die "missing payload: $path"
  done
  [[ -n "$(find "$APP/Contents/Resources" -type f -name THIRD-PARTY-NOTICES.md -print -quit)" ]] || die 'missing third-party notices'
  for path in "$APP/Contents/MacOS/$MAIN" "$CORE" "$RESOURCES/sing-box"; do
    validate_macho "$path"
  done
  codesign --verify --deep --strict --all-architectures "$APP" || die 'bundle signature verification failed'
  SIGNATURE="$(codesign -dv "$APP" 2>&1)" || die 'cannot read bundle signature'
  [[ "$(printf '%s\n' "$SIGNATURE" | awk -F= '/^Signature=/ { print $2 }')" == adhoc ]] || die 'expected an ad-hoc bundle signature'
  validate_go_tool

  STAGE="$(mktemp -d "${TMPDIR:-/tmp}/tenebra-pkg.XXXXXX")"
  trap '[[ -z "$STAGE" ]] || /bin/rm -rf "$STAGE"' EXIT
  validate_core_metadata "$CORE" "$STAGE"
  PAYLOAD="$STAGE/payload"
  SCRIPTS="$STAGE/scripts"
  HELPERS="$PAYLOAD/Library/PrivilegedHelperTools/Tenebra"
  mkdir -p "$PAYLOAD/Applications" "$HELPERS" "$PAYLOAD/Library/LaunchDaemons" "$PAYLOAD/Library/Logs/Tenebra" "$SCRIPTS" "$STAGE/packages"
  # Root package directories are 755; mktemp and umask keep all staging private.
  find "$PAYLOAD" -type d -exec chmod 755 {} +
  ditto --rsrc --extattr --acl "$APP" "$PAYLOAD/Applications/Tenebra.app"
  install -m 755 "$CORE" "$HELPERS/tenebra-core"
  install -m 755 "$RESOURCES/sing-box" "$HELPERS/sing-box"
  for name in geoip-ru.srs geosite-ru.srs geosite-ads.srs; do install -m 644 "$RESOURCES/$name" "$HELPERS/$name"; done
  install -m 644 "$REPO_ROOT/deploy/macos/com.tenebra.core.plist" "$PAYLOAD/Library/LaunchDaemons/com.tenebra.core.plist"
  build_healthcheck "$HELPERS/tenebra-healthcheck" "$STAGE"
  for name in preinstall postinstall; do install -m 755 "$REPO_ROOT/packaging/macos/$name" "$SCRIPTS/$name"; done
  sed "s/@VERSION@/$VERSION/g" "$REPO_ROOT/packaging/macos/common.sh" > "$SCRIPTS/common.sh"
  chmod 644 "$SCRIPTS/common.sh"
  cp "$REPO_ROOT/packaging/macos/components.plist" "$STAGE/components.plist"
  pkgbuild --root "$PAYLOAD" --install-location / --identifier com.tenebra.desktop.personal.pkg \
    --version "$VERSION" --ownership recommended --component-plist "$STAGE/components.plist" \
    --scripts "$SCRIPTS" "$STAGE/packages/Tenebra-component.pkg"
  # shellcheck source=packaging/macos/normalize-scripts.sh
  source "$REPO_ROOT/packaging/macos/normalize-scripts.sh"
  normalize_scripts_archive "$STAGE/packages/Tenebra-component.pkg" "$SCRIPTS"
  render_distribution "$REPO_ROOT/packaging/macos/Distribution.xml" "$STAGE/Distribution.xml"
  productbuild --distribution "$STAGE/Distribution.xml" --package-path "$STAGE/packages" "$STAGE/Tenebra.pkg"
  mv "$STAGE/Tenebra.pkg" "$OUTPUT"
  echo "Built $OUTPUT ($ARCH; ad-hoc executables; unsigned Installer package; not notarized)."
}

# Sourcing exposes the same validators used by the CLI without running a build.
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
