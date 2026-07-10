#!/usr/bin/env bash
# Removes the Tenebra privileged daemon installed by install-daemon.sh: it
# unloads the LaunchDaemon and deletes the plist and the privileged helper
# directory. User data under /Library/Application Support/Tenebra and the logs
# are kept by default; pass --purge to remove those too. Requires root; it
# re-execs itself under sudo when needed. See docs/porting/macos.md.
set -euo pipefail

SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)/$(basename "${BASH_SOURCE[0]}")"

readonly LABEL="com.tenebra.core"
readonly HELPER_DIR="/Library/PrivilegedHelperTools/Tenebra"
readonly LOG_DIR="/Library/Logs/Tenebra"
readonly PLIST_DST="/Library/LaunchDaemons/${LABEL}.plist"
readonly DATA_DIR="/Library/Application Support/Tenebra"

PURGE=0

log() { echo "uninstall-daemon: $*" >&2; }
die() { echo "uninstall-daemon: error: $*" >&2; exit 1; }

usage() {
  cat >&2 <<'EOF'
Usage: uninstall-daemon.sh [--purge]

  --purge  Also remove user data (/Library/Application Support/Tenebra) and the
           service logs (/Library/Logs/Tenebra). Without it both are kept.

Unloads and removes the com.tenebra.core LaunchDaemon. Requires root; re-execs
under sudo when needed.
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

# ensure_root re-execs under sudo when not already root: unloading a system
# daemon and deleting under /Library both need it. Arguments (including --purge)
# are preserved across the re-exec.
ensure_root() {
  [[ "${EUID}" -eq 0 ]] && return 0
  log "requesting administrator privileges via sudo"
  exec sudo -- "${SELF}" "$@"
}

# stop_daemon unloads the LaunchDaemon if it is loaded. A not-loaded label is
# fine (a partial or repeated uninstall), so absence is ignored.
stop_daemon() {
  if launchctl print "system/${LABEL}" >/dev/null 2>&1; then
    log "unloading ${LABEL}"
    launchctl bootout "system/${LABEL}" 2>/dev/null || true
  fi
}

# remove_files deletes the plist and the privileged helper directory. Both paths
# are fixed constants, never derived from input, so the rm -rf cannot widen.
remove_files() {
  rm -f "${PLIST_DST}"
  rm -rf "${HELPER_DIR}"
  log "removed ${PLIST_DST} and ${HELPER_DIR}"
}

# purge_data removes user data and logs. Only reached with --purge.
purge_data() {
  rm -rf "${DATA_DIR}"
  rm -rf "${LOG_DIR}"
  log "purged user data (${DATA_DIR}) and logs (${LOG_DIR})"
}

main() {
  parse_args "$@"
  ensure_root "$@"
  stop_daemon
  remove_files
  if [[ "${PURGE}" -eq 1 ]]; then
    purge_data
  else
    log "kept user data (${DATA_DIR}) and logs (${LOG_DIR}); pass --purge to remove them"
  fi
  log "uninstalled ${LABEL}"
}

main "$@"
