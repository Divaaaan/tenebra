#!/usr/bin/env bash
# Installs the Tenebra privileged daemon on Linux: a systemd service that runs
# tenebra-core as root so it can open /dev/net/tun and program routes, and serves
# the control protocol on a unix socket an unprivileged GUI attaches to. This is
# the hand-installed path for any distribution — the counterpart of
# scripts/macos/install-daemon.sh. On Arch there is a packaged path instead
# (packaging/arch/PKGBUILD); see docs/porting/linux.md.
#
# Two source modes for the binaries:
#   --from-dir <dir>   copy them out of a built payload directory
#   --dev              build/collect them from this checkout
#
# Re-run to upgrade in place: it stops the service, replaces the binaries, and
# starts the new ones — and restores the previous install if any step fails.
# Requires root; it re-execs itself under sudo when needed.
set -euo pipefail

# Absolute path to this script and the repo root, resolved before any sudo
# re-exec so they stay valid regardless of the caller's working directory.
SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)/$(basename "${BASH_SOURCE[0]}")"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." >/dev/null 2>&1 && pwd)"

# Install destinations. INSTALL_DIR must match the paths baked into the unit
# file; check_unit_paths asserts that rather than trusting it.
readonly UNIT_NAME="tenebra.service"
# /usr/local is the FHS home for locally installed software — the half of the
# hierarchy a distribution's package manager never touches — so a hand-install
# and a distro package (which owns /usr/lib/tenebra) can coexist without either
# overwriting the other.
readonly INSTALL_DIR="/usr/local/lib/tenebra"
readonly UNIT_DST="/etc/systemd/system/${UNIT_NAME}"
readonly PACKAGE_UNIT="/usr/lib/systemd/system/${UNIT_NAME}"
readonly DATA_DIR="/var/lib/tenebra/data"
readonly SOCKET_PATH="/run/tenebra.sock"
readonly MODULES_CONF="/etc/modules-load.d/tenebra.conf"

# Source of the unit that gets installed: it ships next to this script's repo.
readonly UNIT_SRC="${REPO_ROOT}/deploy/linux/${UNIT_NAME}"
# Where --dev collects the prebuilt resources fetch-resources.sh writes.
readonly DEV_RESOURCE_DIR="${REPO_ROOT}/ui-desktop/src-tauri/resources"

# Resolved payload, filled by resolve_sources.
MODE=""
SRC_DIR=""
# Set by --allow-unsafe-source: skip the ownership/permission gate on the payload
# directory. Off by default so binaries anyone could have swapped are refused
# rather than installed as a root service; the flag is the deliberate
# dev-convenience escape hatch.
ALLOW_UNSAFE_SOURCE=0
CORE_SRC=""
SINGBOX_SRC=""
RULESET_SRC_DIR=""
# Temp dir holding a --dev core build, removed after install. Only ever set in
# --dev mode so cleanup can never touch a real payload.
DEV_BUILD_TMP=""
# Temp dir holding the previous install, used to roll back a failed upgrade.
BACKUP_DIR=""
# Set once install_payload starts touching the system, so the EXIT trap knows a
# failure left a half-replaced install behind and must be undone.
INSTALL_STARTED=0
# Set when main completes, so the EXIT trap can tell success from failure.
INSTALL_DONE=0

log() { echo "install-daemon: $*" >&2; }
die() { echo "install-daemon: error: $*" >&2; exit 1; }

usage() {
  cat >&2 <<'EOF'
Usage: install-daemon.sh (--from-dir <dir> | --dev)

  --from-dir <dir>  Install tenebra-core, sing-box and the .srs rule-sets from a
                    built payload directory (an unpacked AppImage's app dir, an
                    extracted .deb, or a Tauri bundle directory — a resources/
                    subdirectory is picked up automatically).
  --dev             Build tenebra-core from this checkout and take sing-box plus
                    the rule-sets from ui-desktop/src-tauri/resources. Run
                    scripts/fetch-resources.sh first to populate them.
  --allow-unsafe-source
                    Install even if the payload directory is writable by users
                    other than its owner. For local dev trees on shared
                    machines only; the default refuses, because everything in
                    that directory is about to run as root.

Installs the tenebra systemd service and starts it. Safe to re-run: it upgrades
the binaries in place and rolls back if the upgrade fails. Requires root;
re-execs under sudo when needed.
EOF
}

# parse_args reads the source mode into MODE (and SRC_DIR for --from-dir).
parse_args() {
  while [[ "$#" -gt 0 ]]; do
    case "$1" in
      --from-dir)
        MODE="from-dir"
        SRC_DIR="${2:-}"
        [[ -n "${SRC_DIR}" ]] || die "--from-dir needs a directory"
        shift 2
        ;;
      --dev)
        MODE="dev"
        shift
        ;;
      --allow-unsafe-source)
        ALLOW_UNSAFE_SOURCE=1
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
  [[ -n "${MODE}" ]] || die "choose a source mode: --from-dir <dir> or --dev"
}

# require_systemd fails early and clearly on a machine this script cannot serve,
# instead of laying files down and then discovering systemctl is missing.
# /run/systemd/system is the documented "systemd is the running init" probe — the
# binary being installed is not enough (a container or an OpenRC host can carry
# it without systemd being PID 1).
require_systemd() {
  command -v systemctl >/dev/null 2>&1 \
    || die "systemctl not found; this installer targets systemd (see docs/porting/linux.md for running the core by hand)"
  [[ -d /run/systemd/system ]] \
    || die "systemd is not the running init on this machine; start tenebra-core --socket by hand instead"
}

# resolve_sources fills CORE_SRC, SINGBOX_SRC and RULESET_SRC_DIR for the chosen
# mode. For --dev it builds the core; that build runs as the invoking user
# (before any sudo re-exec) so go uses the user's toolchain and module cache.
resolve_sources() {
  case "${MODE}" in
    from-dir) resolve_from_dir "${SRC_DIR}" ;;
    dev)      resolve_dev ;;
    *)        die "internal: unknown mode ${MODE}" ;;
  esac
}

# resolve_from_dir locates the payload inside a built directory. Two layouts are
# accepted: everything flat in one directory (what an extracted package or this
# repo's resource directory looks like), and a Tauri bundle, which keeps the
# sidecar beside the executable and the bundled resources in resources/.
resolve_from_dir() {
  local dir="$1"
  [[ -d "${dir}" ]] || die "no such directory: ${dir}"
  verify_source_trust "${dir}"

  CORE_SRC="${dir}/tenebra-core"
  SINGBOX_SRC="${dir}/sing-box"
  RULESET_SRC_DIR="${dir}"
  if [[ ! -f "${SINGBOX_SRC}" && -f "${dir}/resources/sing-box" ]]; then
    verify_source_trust "${dir}/resources"
    SINGBOX_SRC="${dir}/resources/sing-box"
    RULESET_SRC_DIR="${dir}/resources"
  fi

  [[ -f "${CORE_SRC}" ]] || die "tenebra-core not found at ${CORE_SRC}"
  [[ -f "${SINGBOX_SRC}" ]] || die "sing-box not found at ${SINGBOX_SRC} (nor in ${dir}/resources)"
}

# verify_source_trust refuses a payload directory that users other than its owner
# can write to. Linux has no Gatekeeper to ask — there is no platform signature
# on a plain ELF the way macOS's install script can lean on codesign/spctl — so
# the checkable property left is who could have put these files here. Everything
# in this directory is about to be copied into a root service, so a
# group- or world-writable directory means any local user could have swapped the
# binary that then runs as root. Refusing is fail-closed;
# --allow-unsafe-source is the explicit, logged escape hatch.
verify_source_trust() {
  local dir="$1" perm
  if [[ "${ALLOW_UNSAFE_SOURCE}" -eq 1 ]]; then
    log "warning: --allow-unsafe-source set; not checking who can write to ${dir}"
    log "warning: only do this for a directory you control — anything there installs as code that runs as root"
    return 0
  fi
  perm="$(stat -c '%a' "${dir}")" || die "cannot stat ${dir}"
  if (( 0"${perm}" & 0022 )); then
    die "${dir} is group/other-writable (mode ${perm}); another local user could swap the binaries this installs as root. Fix the permissions, or pass --allow-unsafe-source."
  fi
}

# validate_handoff_core hardens the --dev sudo hand-off. resolve_dev's fast path
# trusts _TENEBRA_CORE_BIN to name the core built moments earlier by the invoking
# user (build-as-user keeps go on that user's toolchain and module cache). But
# the environment is an attacker-influenceable channel: a sudoers rule that
# permits this script, or an env_keep entry, would let someone seed the variable
# with a planted binary that we would then install as a root service — straight
# privilege escalation. So the elevated pass refuses to install anything it
# cannot tie back to a build its own non-root half could have produced: a regular
# file (never a symlink) named tenebra-core, inside a tenebra-core-build.* temp
# dir under the expected TMPDIR, owned by the very user who invoked sudo, in a
# directory that user owns and no other user can write. A path pointing anywhere
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
  # resolve_dev's mktemp uses. Both sides are canonicalised so a symlinked TMPDIR
  # does not defeat the match.
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
  owner="$(stat -c '%u' "${build_dir}")" || die "cannot stat ${build_dir}"
  [[ "${owner}" == "${invoker}" ]] \
    || die "refusing _TENEBRA_CORE_BIN hand-off: ${build_dir} owned by uid ${owner}, not the invoking user (uid ${invoker})"
  owner="$(stat -c '%u' "${real}")" || die "cannot stat ${real}"
  [[ "${owner}" == "${invoker}" ]] \
    || die "refusing _TENEBRA_CORE_BIN hand-off: ${real} owned by uid ${owner}, not the invoking user (uid ${invoker})"
  perm="$(stat -c '%a' "${build_dir}")" || die "cannot stat ${build_dir}"
  if (( 0"${perm}" & 0022 )); then
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
    command -v go >/dev/null 2>&1 || die "go not found on PATH; install Go or use --from-dir"
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
# after this point needs root: writing under /usr/local and /etc, loading the tun
# module and opening the device all do. The --dev build has already run as the
# invoking user; its result is forwarded so the elevated pass reuses it instead
# of rebuilding as root.
ensure_root() {
  [[ "${EUID}" -eq 0 ]] && return 0
  log "requesting administrator privileges via sudo"
  if [[ "${MODE}" == "dev" ]]; then
    exec sudo "_TENEBRA_CORE_BIN=${CORE_SRC}" -- "${SELF}" "$@"
  else
    exec sudo -- "${SELF}" "$@"
  fi
}

# check_unit_paths refuses to install a unit that points somewhere other than
# where this script puts the binaries. The two files are edited independently, so
# a drifted path would otherwise surface as a service that starts, fails and
# restarts forever with a bare "No such file or directory".
check_unit_paths() {
  [[ -f "${UNIT_SRC}" ]] || die "unit not found at ${UNIT_SRC}"
  grep -qF "ExecStart=${INSTALL_DIR}/tenebra-core" "${UNIT_SRC}" \
    || die "${UNIT_SRC} does not start ${INSTALL_DIR}/tenebra-core; the unit and this installer disagree on the install prefix"
  grep -qF "TENEBRA_SINGBOX=${INSTALL_DIR}/sing-box" "${UNIT_SRC}" \
    || die "${UNIT_SRC} does not point TENEBRA_SINGBOX at ${INSTALL_DIR}/sing-box; the unit and this installer disagree on the install prefix"
}

# warn_packaged_install points out an existing distro package. A unit in
# /etc/systemd/system shadows the one a package ships in /usr/lib/systemd/system
# — systemd's own precedence rule — so the hand-install silently wins and pacman
# would later update binaries nothing runs. That is confusing enough to be worth
# saying out loud, but it is a legitimate thing to do deliberately, so it is a
# warning and not a refusal.
warn_packaged_install() {
  [[ -f "${PACKAGE_UNIT}" ]] || return 0
  log "warning: ${PACKAGE_UNIT} exists, so Tenebra is already installed as a distribution package"
  log "warning: ${UNIT_DST} takes precedence over it; uninstall the package, or remove ${UNIT_DST} to go back to it"
}

# ensure_tun_module makes /dev/net/tun available before the service needs it. The
# unit deliberately keeps CAP_SYS_MODULE out of its bounding set, so the daemon
# cannot pull the module in itself, and on a kernel that ships tun as a module
# and has not loaded it yet the first connect would fail with an opaque ENODEV.
# Loading it now covers this boot; the modules-load.d drop-in covers every later
# one. Neither is fatal — plenty of kernels have tun built in, where both steps
# are simply redundant.
ensure_tun_module() {
  if [[ ! -c /dev/net/tun ]]; then
    if modprobe tun 2>/dev/null; then
      log "loaded the tun kernel module"
    else
      log "warning: could not load the tun module and /dev/net/tun is absent; the tunnel will not open until it is available"
    fi
  fi
  install -d -o root -g root -m 755 "$(dirname "${MODULES_CONF}")"
  printf '# Tenebra opens /dev/net/tun; make sure the module is present at boot.\ntun\n' >"${MODULES_CONF}"
  chmod 644 "${MODULES_CONF}"
}

# stop_running stops an active service before its binaries are replaced. This is
# not optional on Linux the way it is on macOS: overwriting a running executable
# fails outright with ETXTBSY, so an upgrade that skipped this would abort
# halfway. It also means an established tunnel drops here — the daemon owns it,
# so replacing the daemon necessarily interrupts it.
stop_running() {
  systemctl is-active --quiet "${UNIT_NAME}" || return 0
  log "stopping the running service (an established tunnel will drop)"
  systemctl stop "${UNIT_NAME}"
}

# stage_previous copies the current install aside so a failure can put it back.
# Only the pieces this script replaces are kept: the payload directory and the
# unit file. The data directory is never touched by an upgrade, so it needs no
# backup.
stage_previous() {
  BACKUP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/tenebra-install-backup.XXXXXX")"
  if [[ -d "${INSTALL_DIR}" ]]; then
    cp -a "${INSTALL_DIR}" "${BACKUP_DIR}/payload"
  fi
  if [[ -f "${UNIT_DST}" ]]; then
    cp -a "${UNIT_DST}" "${BACKUP_DIR}/${UNIT_NAME}"
  fi
}

# rollback restores the staged install after a failed upgrade, so a broken run
# leaves the machine on the daemon it had rather than on a half-replaced one. A
# first install has nothing to restore: it removes what it managed to create
# instead, which is equally "back where we started".
rollback() {
  log "install failed; restoring the previous state"
  systemctl stop "${UNIT_NAME}" 2>/dev/null || true
  rm -rf "${INSTALL_DIR}"
  if [[ -d "${BACKUP_DIR}/payload" ]]; then
    cp -a "${BACKUP_DIR}/payload" "${INSTALL_DIR}"
  fi
  if [[ -f "${BACKUP_DIR}/${UNIT_NAME}" ]]; then
    cp -a "${BACKUP_DIR}/${UNIT_NAME}" "${UNIT_DST}"
  else
    rm -f "${UNIT_DST}"
  fi
  systemctl daemon-reload || true
  # Only bring the service back if there is something to bring back; after a
  # failed first install there is not, and enabling a removed unit would fail.
  if [[ -f "${UNIT_DST}" ]]; then
    systemctl start "${UNIT_NAME}" 2>/dev/null || log "warning: the restored service did not start; check 'journalctl -u ${UNIT_NAME}'"
  fi
  log "restored"
}

# install_payload lays down the binaries and rule-sets with the ownership and
# modes a root service expects.
install_payload() {
  log "installing binaries into ${INSTALL_DIR}"
  # root:root 755 directory, so only root can replace what systemd runs as root.
  install -d -o root -g root -m 755 "${INSTALL_DIR}"
  # Executables 755; overwriting in place is what makes a re-run an upgrade.
  install -o root -g root -m 755 "${CORE_SRC}" "${INSTALL_DIR}/tenebra-core"
  install -o root -g root -m 755 "${SINGBOX_SRC}" "${INSTALL_DIR}/sing-box"
  install_rulesets
  # The machine-scoped store. The core creates and clamps this itself on every
  # start, and the unit's StateDirectory= would too; creating it here as well
  # means the layout is right and inspectable before the service has ever run.
  install -d -o root -g root -m 700 "$(dirname "${DATA_DIR}")"
  install -d -o root -g root -m 700 "${DATA_DIR}"
}

# install_rulesets copies every .srs rule-set found beside the source binaries.
# The core only loads them locally when the full set is present, otherwise it
# falls back to a remote download; a missing set is therefore a warning, not a
# failure.
install_rulesets() {
  local found=0 f
  shopt -s nullglob
  for f in "${RULESET_SRC_DIR}"/*.srs; do
    install -o root -g root -m 644 "${f}" "${INSTALL_DIR}/$(basename "${f}")"
    found=1
  done
  shopt -u nullglob
  if [[ "${found}" -eq 0 ]]; then
    log "warning: no .srs rule-sets in ${RULESET_SRC_DIR}; smart routing will download them at connect time"
  fi
}

# install_unit copies the service file into /etc/systemd/system, the location
# reserved for units the administrator installs (as opposed to /usr/lib, which
# belongs to the package manager). 644 root:root is what systemd expects of a
# unit it will run as root.
install_unit() {
  log "installing ${UNIT_NAME} to ${UNIT_DST}"
  install -o root -g root -m 644 "${UNIT_SRC}" "${UNIT_DST}"
}

# enable_service reloads systemd's view of the unit and starts it, enabling it so
# the daemon comes back after a reboot. `enable --now` is idempotent: on a re-run
# the symlink is already there and only the start happens — and the start is what
# an upgrade needs, because stop_running has already put the service down to free
# the binaries it replaced.
enable_service() {
  systemctl daemon-reload
  log "enabling and starting ${UNIT_NAME}"
  systemctl enable --now "${UNIT_NAME}"
}

# await_socket polls briefly for the control socket. Its appearance is the crisp
# "the daemon is up and answering" signal; absence after the budget means the
# core failed to start, so point the user at the journal instead of claiming
# success.
await_socket() {
  local waited=0
  local budget=10
  while [[ "${waited}" -lt "${budget}" ]]; do
    if [[ -S "${SOCKET_PATH}" ]]; then
      log "done: ${UNIT_NAME} is running and ${SOCKET_PATH} is live"
      return 0
    fi
    sleep 1
    waited=$((waited + 1))
  done
  log "the control socket ${SOCKET_PATH} did not appear within ${budget}s"
  log "inspect 'journalctl -u ${UNIT_NAME} -n 50' and 'systemctl status ${UNIT_NAME}'"
  return 1
}

# cleanup removes the --dev build's temp dir and the upgrade backup, and rolls
# back if the run died after it started replacing files. DEV_BUILD_TMP is only
# ever set in --dev mode, so this can never delete a real payload; exec (in
# ensure_root) does not fire an EXIT trap, so a temp built before the sudo
# re-exec survives into the elevated pass and is cleaned up there.
cleanup() {
  if [[ "${INSTALL_DONE}" -eq 0 && "${INSTALL_STARTED}" -eq 1 ]]; then
    rollback
  fi
  if [[ -n "${DEV_BUILD_TMP}" ]] && [[ -d "${DEV_BUILD_TMP}" ]]; then
    rm -rf "${DEV_BUILD_TMP}"
  fi
  if [[ -n "${BACKUP_DIR}" ]] && [[ -d "${BACKUP_DIR}" ]]; then
    rm -rf "${BACKUP_DIR}"
  fi
}

main() {
  trap cleanup EXIT
  parse_args "$@"
  require_systemd
  resolve_sources
  ensure_root "$@"
  check_unit_paths
  warn_packaged_install
  stage_previous
  INSTALL_STARTED=1
  stop_running
  ensure_tun_module
  install_payload
  install_unit
  enable_service
  # From here the install itself is complete, so a socket that never shows up is
  # reported rather than rolled back: the operator needs the failed daemon and
  # its journal to diagnose, not a silent revert to the previous binaries.
  INSTALL_DONE=1
  await_socket
}

main "$@"
