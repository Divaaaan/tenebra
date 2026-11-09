#!/bin/bash
# Shared by root-owned Installer scripts. No network or profile-store operations.
# Tests source these functions with a disposable prefix and fake launchctl.
ROOT_UID=0
LAUNCHCTL=/bin/launchctl
HEALTH_ATTEMPTS=10
EXPECTED_VERSION='@VERSION@'
HELPER_FILES=(tenebra-core sing-box geoip-ru.srs geosite-ru.srs geosite-ads.srs tenebra-healthcheck)

configure_paths() {
  local prefix="$1"
  APP="$prefix/Applications/Tenebra.app"
  HELPER="$prefix/Library/PrivilegedHelperTools/Tenebra"
  PLIST="$prefix/Library/LaunchDaemons/com.tenebra.core.plist"
  LOG_DIR="$prefix/Library/Logs/Tenebra"
  BACKUP="$prefix/Library/PrivilegedHelperTools/.Tenebra-install-backup"
  SYSTEM_PREFIX="$prefix"
}

log() { echo "Tenebra installer: $*" >&2; }
root_own() { /usr/sbin/chown 0:0 "$@"; }

safe_directory() {
  local path="$1" allow_group_write="${2:-no}" mode
  [[ -d "$path" && ! -L "$path" ]] || { log "unsafe directory: $path"; return 1; }
  [[ "$(/usr/bin/stat -f %u "$path")" == "$ROOT_UID" ]] || { log "directory is not root owned: $path"; return 1; }
  mode="$(/usr/bin/stat -f %Lp "$path")" || return 1
  (( (8#$mode & 2) == 0 )) || { log "directory is writable by everyone: $path"; return 1; }
  if [[ "$allow_group_write" != yes ]]; then
    (( (8#$mode & 16) == 0 )) || { log "directory is writable by a group: $path"; return 1; }
  fi
}

no_links_or_special_files() {
  local path="$1" bad
  [[ ! -L "$path" ]] || { log "refusing symbolic link: $path"; return 1; }
  if [[ -d "$path" ]]; then
    bad="$(/usr/bin/find "$path" ! -type d ! -type f -print -quit)" || return 1
    [[ -z "$bad" ]] || { log "payload contains a link or special file"; return 1; }
  elif [[ -e "$path" && ! -f "$path" ]]; then
    log "payload is not a regular file"; return 1
  fi
}

safe_root_file() {
  local path="$1" mode
  [[ -f "$path" && ! -L "$path" ]] || return 1
  [[ "$(/usr/bin/stat -f %u "$path")" == "$ROOT_UID" ]] || return 1
  [[ "$(/usr/bin/stat -f %l "$path")" == 1 ]] || return 1
  mode="$(/usr/bin/stat -f %Lp "$path")" || return 1
  (( (8#$mode & 18) == 0 ))
}

validate_destinations() {
  local dir name
  safe_directory "$SYSTEM_PREFIX/Applications" yes || return 1
  for dir in Library Library/PrivilegedHelperTools Library/LaunchDaemons Library/Logs; do
    safe_directory "$SYSTEM_PREFIX/$dir" || return 1
  done
  no_links_or_special_files "$APP" || return 1
  [[ ! -e "$APP" || -d "$APP" ]] || return 1
  for dir in "$HELPER" "$LOG_DIR"; do
    [[ ! -L "$dir" ]] || return 1
    if [[ -e "$dir" ]]; then safe_directory "$dir" || return 1; fi
  done
  for name in "${HELPER_FILES[@]}"; do
    no_links_or_special_files "$HELPER/$name" || return 1
    if [[ -e "$HELPER/$name" ]]; then safe_root_file "$HELPER/$name" || return 1; fi
  done
  no_links_or_special_files "$PLIST" || return 1
  if [[ -e "$PLIST" ]]; then safe_root_file "$PLIST" || return 1; fi
  # launchd opens this log as root. Never follow an existing log symlink.
  no_links_or_special_files "$LOG_DIR/service.log" || return 1
  if [[ -e "$LOG_DIR/service.log" ]]; then safe_root_file "$LOG_DIR/service.log" || return 1; fi
}

service_loaded() { "$LAUNCHCTL" print system/com.tenebra.core >/dev/null 2>&1; }

stop_service() {
  if service_loaded; then
    "$LAUNCHCTL" bootout system/com.tenebra.core >/dev/null 2>&1 || return 1
  fi
}

start_service() {
  local attempt
  for attempt in 1 2 3 4 5; do
    if "$LAUNCHCTL" bootstrap system "$PLIST" >/dev/null 2>&1; then
      "$LAUNCHCTL" kickstart -k system/com.tenebra.core >/dev/null 2>&1
      return $?
    fi
    [[ "$attempt" == 5 ]] || /bin/sleep 1
  done
  return 1
}

snapshot_payload() {
  local name
  /bin/mkdir -m 700 "$BACKUP" || return 1
  root_own "$BACKUP" || return 1
  /bin/mkdir -m 700 "$BACKUP/helpers" || return 1
  if [[ -e "$APP" ]]; then
    /usr/bin/ditto --rsrc --extattr --acl "$APP" "$BACKUP/app" || return 1
    /usr/bin/touch "$BACKUP/had-app" || return 1
  fi
  if [[ -e "$PLIST" ]]; then
    /bin/cp -p "$PLIST" "$BACKUP/plist" || return 1
    /usr/bin/touch "$BACKUP/had-plist" || return 1
  fi
  for name in "${HELPER_FILES[@]}"; do
    if [[ -e "$HELPER/$name" ]]; then
      /bin/cp -p "$HELPER/$name" "$BACKUP/helpers/$name" || return 1
    fi
  done
  if [[ -e "$HELPER" ]]; then /usr/bin/touch "$BACKUP/had-helper-dir" || return 1; fi
  if [[ -e "$LOG_DIR" ]]; then /usr/bin/touch "$BACKUP/had-log-dir" || return 1; fi
  if service_loaded; then /usr/bin/touch "$BACKUP/was-loaded" || return 1; fi
  # Commit the snapshot before touching launchd or any live payload.
  /usr/bin/touch "$BACKUP/ready"
}

validate_backup() {
  local mode
  safe_directory "$BACKUP" || return 1
  mode="$(/usr/bin/stat -f %Lp "$BACKUP")" || return 1
  [[ "$mode" == 700 ]] || { log "backup permissions are unsafe"; return 1; }
  no_links_or_special_files "$BACKUP" || return 1
  safe_root_file "$BACKUP/ready" || { log "backup is incomplete"; return 1; }
}

prepare_install() {
  validate_destinations || return 1
  if service_loaded && [[ ! -f "$PLIST" ]]; then
    log "loaded daemon has no restorable plist; refusing to stop it"
    return 1
  fi
  if [[ -e "$BACKUP" || -L "$BACKUP" ]]; then
    log "prior installer backup exists; refusing to overwrite recovery state: $BACKUP"
    return 1
  fi
  if ! snapshot_payload; then
    # No live payload or service has changed yet; retain partial state for diagnosis.
    log "snapshot failed; existing installation is unchanged; backup retained"
    return 1
  fi
  if ! stop_service; then
    log "could not stop prior service; attempting restoration"
    rollback_install || return 1
    return 1
  fi
}

restore_payload() {
  local name
  # Destinations are checked again before any removal; data lives elsewhere and
  # is never traversed. Only these managed payload paths can be removed.
  validate_destinations || return 1
  /bin/rm -rf "$APP" || return 1
  if [[ -f "$BACKUP/had-app" ]]; then
    /usr/bin/ditto --rsrc --extattr --acl "$BACKUP/app" "$APP" || return 1
  fi
  /bin/rm -f "$PLIST" || return 1
  if [[ -f "$BACKUP/had-plist" ]]; then /bin/cp -p "$BACKUP/plist" "$PLIST" || return 1; fi
  for name in "${HELPER_FILES[@]}"; do
    /bin/rm -f "$HELPER/$name" || return 1
    if [[ -f "$BACKUP/helpers/$name" ]]; then
      /bin/cp -p "$BACKUP/helpers/$name" "$HELPER/$name" || return 1
    fi
  done
  if [[ ! -f "$BACKUP/had-helper-dir" ]]; then /bin/rmdir "$HELPER" 2>/dev/null || :; fi
  # Preserve logs written during the attempted start; remove only an empty new directory.
  if [[ ! -f "$BACKUP/had-log-dir" ]]; then /bin/rmdir "$LOG_DIR" 2>/dev/null || :; fi
}

rollback_install() {
  validate_backup || return 1
  stop_service || { log "rollback could not stop daemon; backup retained"; return 1; }
  restore_payload || { log "rollback payload restore failed; backup retained"; return 1; }
  if [[ -f "$BACKUP/was-loaded" ]]; then
    start_service || { log "rollback could not reload previous daemon; backup retained"; return 1; }
  fi
  /bin/rm -rf "$BACKUP" || return 1
  log "previous executable payload and launchd registration restored"
}

activate_new_payload() {
  local name attempt
  validate_destinations || return 1
  for name in "${HELPER_FILES[@]}"; do safe_root_file "$HELPER/$name" || return 1; done
  safe_root_file "$PLIST" || return 1
  stop_service || return 1
  start_service || return 1
  for ((attempt=1; attempt<=HEALTH_ATTEMPTS; attempt++)); do
    if "$HELPER/tenebra-healthcheck" --expected-version "$EXPECTED_VERSION" --timeout 1s >/dev/null 2>&1; then return 0; fi
    [[ "$attempt" == "$HEALTH_ATTEMPTS" ]] || /bin/sleep 1
  done
  log "new daemon did not answer a matching successful status response"
  return 1
}

finish_install() {
  validate_backup || return 1
  if ! activate_new_payload; then
    log "installation readiness failed; restoring prior executable payload"
    rollback_install || { log "automatic rollback failed; protected backup retained at $BACKUP"; return 1; }
    return 1
  fi
  /bin/rm -rf "$BACKUP" || return 1
  log "daemon protocol readiness verified"
}

require_install_environment() {
  [[ "$EUID" == 0 ]] || { log "requires System Installer administrator authentication"; return 1; }
  [[ "${2:-}" == / && "${3:-}" == / ]] || { log "install only onto the running startup volume at /"; return 1; }
}
