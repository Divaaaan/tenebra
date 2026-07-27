#!/usr/bin/env bash
# Removes the Tenebra privileged daemon installed by install-daemon.sh: it stops
# and disables the systemd service and deletes the unit, the binaries and the
# tun modules-load drop-in. User data under /var/lib/tenebra is kept by default;
# pass --purge to remove it too. Requires root; it re-execs itself under sudo
# when needed. See docs/porting/linux.md.
#
# This removes a hand-install only. A Tenebra installed from a distribution
# package is removed with that package manager (`pacman -Rns tenebra` on Arch);
# the paths below are deliberately the /usr/local ones a package never owns.
set -euo pipefail

SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)/$(basename "${BASH_SOURCE[0]}")"

readonly UNIT_NAME="tenebra.service"
readonly INSTALL_DIR="/usr/local/lib/tenebra"
readonly UNIT_DST="/etc/systemd/system/${UNIT_NAME}"
readonly PACKAGE_UNIT="/usr/lib/systemd/system/${UNIT_NAME}"
readonly DATA_PARENT="/var/lib/tenebra"
readonly MODULES_CONF="/etc/modules-load.d/tenebra.conf"

PURGE=0

log() { echo "uninstall-daemon: $*" >&2; }
die() { echo "uninstall-daemon: error: $*" >&2; exit 1; }

usage() {
  cat >&2 <<'EOF'
Usage: uninstall-daemon.sh [--purge]

  --purge  Also remove user data (/var/lib/tenebra), which holds your profiles
           and their subscription credentials. Without it the data is kept, so a
           later re-install picks up where you left off.

Stops, disables and removes the tenebra systemd service. Requires root;
re-execs under sudo when needed.
EOF
}

parse_args() {
  while [[ "$#" -gt 0 ]]; do
    case "$1" in
      --purge)
        PURGE=1
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
}

# ensure_root re-execs under sudo when not already root: stopping a system
# service and deleting under /usr/local and /etc both need it. Arguments
# (including --purge) are preserved across the re-exec.
ensure_root() {
  [[ "${EUID}" -eq 0 ]] && return 0
  log "requesting administrator privileges via sudo"
  exec sudo -- "${SELF}" "$@"
}

# stop_daemon stops and disables the service if systemd knows about it. Every
# step tolerates absence, because a partial or repeated uninstall is a normal
# thing to run. With no systemctl at all there is nothing to stop — the files are
# still removed below.
stop_daemon() {
  command -v systemctl >/dev/null 2>&1 || return 0
  if systemctl is-active --quiet "${UNIT_NAME}"; then
    log "stopping ${UNIT_NAME} (an established tunnel will drop)"
    systemctl stop "${UNIT_NAME}" || true
  fi
  # disable removes the multi-user.target want; harmless when it was never
  # enabled, and required so a leftover symlink does not point at a deleted unit.
  systemctl disable "${UNIT_NAME}" >/dev/null 2>&1 || true
}

# remove_files deletes the unit, the binaries and the modules-load drop-in. Every
# path is a fixed constant, never derived from input, so the rm -rf cannot widen.
# daemon-reload afterwards is what makes systemd forget the unit it just lost;
# reset-failed clears a failed state a crashing daemon may have left behind, so
# `systemctl status` does not keep reporting a service that no longer exists.
remove_files() {
  rm -f "${UNIT_DST}"
  rm -rf "${INSTALL_DIR}"
  rm -f "${MODULES_CONF}"
  log "removed ${UNIT_DST}, ${INSTALL_DIR} and ${MODULES_CONF}"
  if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
    systemctl reset-failed "${UNIT_NAME}" >/dev/null 2>&1 || true
  fi
}

# warn_packaged_install fires when a distribution package is still installed. The
# hand-installed unit in /etc was shadowing the packaged one; removing it hands
# control back to the package, which is very likely not what someone running an
# uninstall script expects to happen.
warn_packaged_install() {
  [[ -f "${PACKAGE_UNIT}" ]] || return 0
  log "note: ${PACKAGE_UNIT} is still present, so the packaged Tenebra remains installed"
  log "note: it is no longer shadowed — remove it with your package manager if you wanted everything gone"
}

# purge_data removes the machine-scoped store. Only reached with --purge, because
# it destroys the imported profiles rather than just the software.
purge_data() {
  rm -rf "${DATA_PARENT}"
  log "purged user data (${DATA_PARENT})"
}

main() {
  parse_args "$@"
  ensure_root "$@"
  stop_daemon
  remove_files
  if [[ "${PURGE}" -eq 1 ]]; then
    purge_data
  else
    log "kept user data (${DATA_PARENT}); pass --purge to remove it"
  fi
  warn_packaged_install
  log "uninstalled ${UNIT_NAME}"
}

main "$@"
