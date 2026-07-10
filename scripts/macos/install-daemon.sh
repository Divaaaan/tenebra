#!/usr/bin/env bash
# Installs the Tenebra privileged daemon on macOS: a LaunchDaemon that runs
# tenebra-core as root so it can open the utun device, and serves the control
# protocol on a unix socket an unprivileged GUI attaches to. This is the
# hand-installed dev/personal path — one sudo install, no Apple signing
# ceremony. The signed production build will register an equivalent daemon
# through SMAppService instead; see docs/porting/macos.md.
#
# Two source modes for the binaries:
#   --from-app <path-to-Tenebra.app>   copy them out of a built app bundle
#   --dev                              build/collect them from this checkout
#
# Re-run to upgrade in place: it stops the daemon, replaces the binaries, and
# starts the new ones. Requires root; it re-execs itself under sudo when needed.
set -euo pipefail

# Absolute path to this script and the repo root, resolved before any sudo
# re-exec so they stay valid regardless of the caller's working directory.
SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)/$(basename "${BASH_SOURCE[0]}")"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." >/dev/null 2>&1 && pwd)"

# Install destinations. These match the paths baked into the LaunchDaemon plist.
readonly LABEL="com.tenebra.core"
readonly HELPER_PARENT="/Library/PrivilegedHelperTools"
readonly HELPER_DIR="${HELPER_PARENT}/Tenebra"
readonly LOG_DIR="/Library/Logs/Tenebra"
readonly PLIST_DST="/Library/LaunchDaemons/${LABEL}.plist"
readonly SOCKET_PATH="/var/run/tenebra.sock"

# Source of the plist that gets installed: it ships next to this script's repo.
readonly PLIST_SRC="${REPO_ROOT}/deploy/macos/com.tenebra.core.plist"
# Where --dev collects the prebuilt resources fetch-resources.sh writes.
readonly DEV_RESOURCE_DIR="${REPO_ROOT}/ui-desktop/src-tauri/resources"

# Resolved payload, filled by resolve_sources.
MODE=""
APP_PATH=""
# Set by --allow-unsigned: skip the codesign/spctl gate on a --from-app bundle.
# Off by default so an unsigned or tampered bundle is refused, not installed as a
# root LaunchDaemon; the flag is the deliberate dev-convenience escape hatch.
ALLOW_UNSIGNED=0
CORE_SRC=""
SINGBOX_SRC=""
RULESET_SRC_DIR=""
# Temp dir holding a --dev core build, removed after install. Only ever set in
# --dev mode so cleanup can never touch a real bundle.
DEV_BUILD_TMP=""

log() { echo "install-daemon: $*" >&2; }
die() { echo "install-daemon: error: $*" >&2; exit 1; }

usage() {
  cat >&2 <<'EOF'
Usage: install-daemon.sh (--from-app <path-to-Tenebra.app> | --dev)

  --from-app <path>  Install tenebra-core, sing-box and the .srs rule-sets from a
                     built Tenebra.app bundle.
  --dev              Build tenebra-core from this checkout and take sing-box plus
                     the rule-sets from ui-desktop/src-tauri/resources. Run
                     scripts/fetch-resources.sh first to populate them.
  --allow-unsigned   With --from-app: install even if the bundle fails codesign
                     verification / Gatekeeper assessment. For local dev bundles
                     only; the default refuses an unsigned or tampered bundle.

Installs the com.tenebra.core LaunchDaemon and starts it. Safe to re-run: it
upgrades the binaries in place. Requires root; re-execs under sudo when needed.
EOF
}

# parse_args reads the source mode into MODE (and APP_PATH for --from-app).
parse_args() {
  while [[ "$#" -gt 0 ]]; do
    case "$1" in
      --from-app)
        MODE="from-app"
        APP_PATH="${2:-}"
        [[ -n "${APP_PATH}" ]] || die "--from-app needs a path to Tenebra.app"
        shift 2
        ;;
      --dev)
        MODE="dev"
        shift
        ;;
      --allow-unsigned)
        ALLOW_UNSIGNED=1
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        die "unknown argument: $1 (see --help)"
        ;;
    esac
  done
  [[ -n "${MODE}" ]] || die "choose a source mode: --from-app <path-to-Tenebra.app> or --dev"
}

# resolve_sources fills CORE_SRC, SINGBOX_SRC and RULESET_SRC_DIR for the chosen
# mode. For --dev it builds the core; that build runs as the invoking user
# (before any sudo re-exec) so go uses the user's toolchain and module cache.
resolve_sources() {
  case "${MODE}" in
    from-app) resolve_from_app "${APP_PATH}" ;;
    dev)      resolve_dev ;;
    *)        die "internal: unknown mode ${MODE}" ;;
  esac
}

# resolve_from_app locates the payload inside a built Tenebra.app.
#
# Tauri v2 bundle layout (verified against the Tauri v2 distribution docs): an
# externalBin sidecar is placed in Contents/MacOS beside the app executable,
# while bundled resources keep their declared subfolder under Contents/Resources
# — so the resources/ entries land in Contents/Resources/resources. tenebra-core
# is the sidecar; sing-box and the .srs sets are resources. If a future Tauri
# release moves these, the checks below fail at install time with the exact path
# that was expected, rather than installing a half-populated helper directory.
#
# NOTE: this exact layout still wants confirming against a real universal bundle
# built by the (parallel) macOS bundling work; the paths are the documented
# behaviour, not yet an observed one.
resolve_from_app() {
  local app="$1"
  [[ -d "${app}" ]] || die "no such app bundle: ${app}"
  verify_app_signature "${app}"
  local macos_dir="${app}/Contents/MacOS"
  local res_dir="${app}/Contents/Resources/resources"

  CORE_SRC="${macos_dir}/tenebra-core"
  SINGBOX_SRC="${res_dir}/sing-box"
  RULESET_SRC_DIR="${res_dir}"

  [[ -f "${CORE_SRC}" ]] || die "tenebra-core not found at ${CORE_SRC} (expected the Tauri externalBin sidecar in Contents/MacOS)"
  [[ -f "${SINGBOX_SRC}" ]] || die "sing-box not found at ${SINGBOX_SRC} (expected a Tauri resource in Contents/Resources/resources)"
}

# verify_app_signature refuses to install binaries out of an unsigned or tampered
# .app before they become a root LaunchDaemon. --from-app takes tenebra-core and
# sing-box from an operator-supplied bundle and copies them verbatim into
# /Library/PrivilegedHelperTools; with no check, a swapped-in binary in a doctored
# bundle would run as root. codesign --verify --deep --strict re-hashes the whole
# bundle and every nested Mach-O (the sidecar core and the sing-box resource)
# against the signature, so any post-signing edit — or the absence of a signature
# — fails here. spctl then asks Gatekeeper whether the bundle is actually trusted
# for execution (a valid Developer ID, notarised), rejecting ad-hoc/self-signed
# seals a bare codesign check would accept. The signed production build passes
# both untouched. --allow-unsigned is the explicit, logged escape hatch for a
# local dev bundle; without it, verification failure is fatal.
verify_app_signature() {
  local app="$1"
  if [[ "${ALLOW_UNSIGNED}" -eq 1 ]]; then
    log "warning: --allow-unsigned set; skipping signature verification of ${app}"
    log "warning: only do this for a bundle you built yourself — an untrusted bundle installs code that runs as root"
    return 0
  fi
  command -v codesign >/dev/null 2>&1 \
    || die "codesign not found; cannot verify ${app}. Pass --allow-unsigned to install a local dev bundle anyway."
  log "verifying code signature of ${app}"
  codesign --verify --deep --strict "${app}" 2>/dev/null \
    || die "signature verification failed for ${app}: unsigned or tampered. Re-sign the bundle, or pass --allow-unsigned to install anyway (dev only)."
  # spctl assessment is a second, stricter gate (Gatekeeper trust, not just a
  # valid seal). Treat its absence as non-fatal — codesign already caught tamper
  # — but a live rejection is fatal, since it means the bundle is not trusted.
  if command -v spctl >/dev/null 2>&1; then
    spctl --assess --type execute "${app}" 2>/dev/null \
      || die "Gatekeeper (spctl) rejected ${app}: not trusted for execution. Notarize/sign it, or pass --allow-unsigned for a local build."
  fi
}

# validate_handoff_core hardens the --dev sudo hand-off. resolve_dev's fast path
# trusts _TENEBRA_CORE_BIN to name the core built moments earlier by the invoking
# user (build-as-user keeps go on that user's toolchain and module cache). But
# the environment is an attacker-influenceable channel: a sudoers rule that
# permits this script, or an env_keep entry, would let someone seed the variable
# with a planted binary that we would then install as a root LaunchDaemon —
# straight privilege escalation. So the elevated pass refuses to install anything
# it cannot tie back to a build its own non-root half could have produced: a
# regular file (never a symlink) named tenebra-core, inside a tenebra-core-build.*
# temp dir under the expected TMPDIR, owned by the very user who invoked sudo, in
# a directory that user owns and no other user can write. A path pointing anywhere
# else — a world-writable drop, another user's file, a symlink to a system binary
# — fails closed here rather than reaching install_payload.
validate_handoff_core() {
  local given="$1"
  # SUDO_UID is set only when we genuinely re-exec'd from a normal user via sudo.
  # Its absence means the variable was set some other way (e.g. straight `sudo
  # env _TENEBRA_CORE_BIN=... install-daemon.sh`), which is exactly the injection
  # we refuse: there is no trusted invoking user to bind the artifact to.
  local invoker="${SUDO_UID:-}"
  [[ -n "${invoker}" && "${invoker}" != "0" ]] \
    || die "refusing _TENEBRA_CORE_BIN hand-off: no unprivileged SUDO_UID. Run 'install-daemon.sh --dev' and let it re-exec; do not set _TENEBRA_CORE_BIN yourself."

  # A final symlink could smuggle in a system binary the checks below would then
  # read through; reject it outright, then resolve the real target.
  [[ ! -L "${given}" ]] || die "refusing _TENEBRA_CORE_BIN hand-off: ${given} is a symlink"
  local real
  real="$(realpath "${given}" 2>/dev/null)" || die "refusing _TENEBRA_CORE_BIN hand-off: cannot resolve ${given}"
  [[ -f "${real}" ]] || die "refusing _TENEBRA_CORE_BIN hand-off: ${real} is not a regular file"
  [[ "$(basename "${real}")" == "tenebra-core" ]] \
    || die "refusing _TENEBRA_CORE_BIN hand-off: unexpected basename $(basename "${real}"), want tenebra-core"

  # Must sit directly inside a tenebra-core-build.* dir under the same temp root
  # resolve_dev's mktemp uses. Both sides are canonicalised so the /tmp ->
  # /private/tmp and $TMPDIR symlinks macOS interposes do not defeat the match.
  local tmp_root build_dir
  tmp_root="$(realpath "${TMPDIR:-/tmp}" 2>/dev/null)" || die "cannot resolve TMPDIR"
  build_dir="$(dirname "${real}")"
  case "${build_dir}" in
    "${tmp_root}"/tenebra-core-build.*) : ;;
    *) die "refusing _TENEBRA_CORE_BIN hand-off: ${real} is not under a tenebra-core-build.* dir in ${tmp_root}" ;;
  esac

  # The build dir and the artifact must be owned by the invoking user, and the
  # dir must not be group/other-writable, so no second party could have swapped
  # the binary in between the build and this install.
  local owner perm
  owner="$(stat -f '%u' "${build_dir}")" || die "cannot stat ${build_dir}"
  [[ "${owner}" == "${invoker}" ]] \
    || die "refusing _TENEBRA_CORE_BIN hand-off: ${build_dir} owned by uid ${owner}, not the invoking user (uid ${invoker})"
  owner="$(stat -f '%u' "${real}")" || die "cannot stat ${real}"
  [[ "${owner}" == "${invoker}" ]] \
    || die "refusing _TENEBRA_CORE_BIN hand-off: ${real} owned by uid ${owner}, not the invoking user (uid ${invoker})"
  perm="$(stat -f '%OLp' "${build_dir}")" || die "cannot stat ${build_dir}"
  if (( 0${perm} & 0022 )); then
    die "refusing _TENEBRA_CORE_BIN hand-off: build dir ${build_dir} is group/other-writable (mode ${perm})"
  fi
}

# resolve_dev builds the core from the checkout and takes sing-box plus the
# rule-sets from the fetched resource directory.
resolve_dev() {
  # A prior sudo re-exec hands the already-built core back through this env var
  # so it is built exactly once, as the invoking user, not again as root. The
  # env channel is attacker-influenceable, so the handed path is validated
  # against what our own non-root half could have produced before it is trusted.
  if [[ -n "${_TENEBRA_CORE_BIN:-}" ]]; then
    validate_handoff_core "${_TENEBRA_CORE_BIN}"
    CORE_SRC="${_TENEBRA_CORE_BIN}"
    DEV_BUILD_TMP="$(dirname "${CORE_SRC}")"
  else
    command -v go >/dev/null 2>&1 || die "go not found on PATH; install Go or use --from-app"
    DEV_BUILD_TMP="$(mktemp -d "${TMPDIR:-/tmp}/tenebra-core-build.XXXXXX")"
    CORE_SRC="${DEV_BUILD_TMP}/tenebra-core"
    log "building tenebra-core from ${REPO_ROOT}"
    ( cd "${REPO_ROOT}" && go build -o "${CORE_SRC}" ./cmd/tenebra-core ) || die "go build failed"
  fi

  SINGBOX_SRC="${DEV_RESOURCE_DIR}/sing-box"
  RULESET_SRC_DIR="${DEV_RESOURCE_DIR}"
  [[ -f "${SINGBOX_SRC}" ]] || die "sing-box not found at ${SINGBOX_SRC}; run scripts/fetch-resources.sh first"
}

# ensure_root re-execs the script under sudo when not already root. Everything
# after this point needs root: writing under /Library and opening utun both do.
# The --dev build has already run as the invoking user; its result is forwarded
# so the elevated pass reuses it instead of rebuilding as root.
ensure_root() {
  [[ "${EUID}" -eq 0 ]] && return 0
  log "requesting administrator privileges via sudo"
  if [[ "${MODE}" == "dev" ]]; then
    exec sudo "_TENEBRA_CORE_BIN=${CORE_SRC}" -- "${SELF}" "$@"
  else
    exec sudo -- "${SELF}" "$@"
  fi
}

# install_payload lays down the binaries and rule-sets with the ownership and
# modes launchd and a privileged helper expect.
install_payload() {
  log "installing binaries into ${HELPER_DIR}"
  # root:wheel 755 directory, so only root can replace what launchd runs as root.
  install -d -o root -g wheel -m 755 "${HELPER_PARENT}"
  install -d -o root -g wheel -m 755 "${HELPER_DIR}"
  # Executables 755; overwriting in place is what makes a re-run an upgrade.
  install -o root -g wheel -m 755 "${CORE_SRC}" "${HELPER_DIR}/tenebra-core"
  install -o root -g wheel -m 755 "${SINGBOX_SRC}" "${HELPER_DIR}/sing-box"
  install_rulesets
  # launchd does not create the parent of a log path; make it before load.
  install -d -o root -g wheel -m 755 "${LOG_DIR}"
}

# install_rulesets copies every .srs rule-set found beside the source binaries.
# The core only loads them locally when the full set is present, otherwise it
# falls back to a remote download; a missing set is therefore a warning, not a
# failure.
install_rulesets() {
  local found=0 f
  shopt -s nullglob
  for f in "${RULESET_SRC_DIR}"/*.srs; do
    install -o root -g wheel -m 644 "${f}" "${HELPER_DIR}/$(basename "${f}")"
    found=1
  done
  shopt -u nullglob
  if [[ "${found}" -eq 0 ]]; then
    log "warning: no .srs rule-sets in ${RULESET_SRC_DIR}; smart routing will download them at connect time"
  fi
}

# install_plist copies the LaunchDaemon plist into place. launchd refuses to load
# a daemon plist that is not owned root:wheel and writable only by root, so
# 644 root:wheel is mandatory, not cosmetic.
install_plist() {
  [[ -f "${PLIST_SRC}" ]] || die "plist not found at ${PLIST_SRC}"
  log "installing LaunchDaemon to ${PLIST_DST}"
  install -o root -g wheel -m 644 "${PLIST_SRC}" "${PLIST_DST}"
}

# reload_daemon tears down any previous instance and (re)loads the new one so an
# upgrade takes effect immediately.
reload_daemon() {
  # bootout a currently-loaded instance so the replaced binaries take over.
  # Absence is fine: guard on print so a first install stays quiet.
  if launchctl print "system/${LABEL}" >/dev/null 2>&1; then
    log "unloading the running daemon"
    launchctl bootout "system/${LABEL}" 2>/dev/null || true
  fi
  # bootstrap can briefly race the just-finished bootout (launchd reports
  # "Input/output error" until the old job fully unloads), so retry a few times.
  local attempt
  for attempt in 1 2 3 4 5; do
    if launchctl bootstrap system "${PLIST_DST}" 2>/dev/null; then
      break
    fi
    if [[ "${attempt}" -eq 5 ]]; then
      die "launchctl bootstrap failed for ${PLIST_DST}; see ${LOG_DIR}/service.log"
    fi
    sleep 1
  done
  # kickstart -k force-restarts the service so a re-run picks up new binaries even
  # if the label was already bootstrapped.
  launchctl kickstart -k "system/${LABEL}" || die "launchctl kickstart failed for system/${LABEL}"
}

# await_socket polls briefly for the control socket. Its appearance is the crisp
# "the daemon is up and answering" signal; absence after the budget means the
# core failed to start, so point the user at the log instead of claiming success.
await_socket() {
  local waited=0
  local budget=10
  while [[ "${waited}" -lt "${budget}" ]]; do
    if [[ -S "${SOCKET_PATH}" ]]; then
      log "done: ${LABEL} is running and ${SOCKET_PATH} is live"
      return 0
    fi
    sleep 1
    waited=$((waited + 1))
  done
  log "the control socket ${SOCKET_PATH} did not appear within ${budget}s"
  log "inspect ${LOG_DIR}/service.log and 'sudo launchctl print system/${LABEL}'"
  return 1
}

# cleanup removes a --dev core build's temp dir. DEV_BUILD_TMP is only ever set
# in --dev mode, so this can never delete a real bundle; it is a no-op otherwise.
# exec (in ensure_root) does not fire an EXIT trap, so a temp built before the
# sudo re-exec survives into the elevated pass and is cleaned up there.
cleanup() {
  if [[ -n "${DEV_BUILD_TMP}" ]] && [[ -d "${DEV_BUILD_TMP}" ]]; then
    rm -rf "${DEV_BUILD_TMP}"
  fi
}

main() {
  trap cleanup EXIT
  parse_args "$@"
  resolve_sources
  ensure_root "$@"
  install_payload
  install_plist
  reload_daemon
  await_socket
}

main "$@"
