; NSIS hooks for the Tenebra installer (bundle > windows > nsis >
; installerHooks). They are compiled into the stock Tauri template, so the
; template's defines (${UNINSTKEY}, ${PRODUCTNAME}, ...), LogicLib and the
; utils.nsh macros are available here. Two jobs beyond what the template does:
;
; 1. The Windows service. tenebra-core runs as the "tenebra" service so the
;    tunnel outlives any one UI process; the installer owns its lifecycle —
;    stop before files are replaced, (re)register and start after they are,
;    unregister on a real uninstall.
;
; 2. Migration off the per-user v0.2.x installs. Those lived under
;    %LOCALAPPDATA%\Tenebra with their registration in HKCU, and the Tauri
;    template never reconciles across install scopes, so upgrading to this
;    per-machine build would otherwise leave a second, stale Tenebra behind.
;    The migration is registry-and-shortcut surgery only, in the elevating
;    user's profile: the old install directory is user-writable, so an
;    elevated installer must not execute anything from it (the old
;    uninstaller included) nor delete through it — a planted binary or
;    junction would run, or redirect the delete, with our privileges. With
;    its entry points gone the leftover directory is inert; removing it is
;    documented as a manual step.
;
; Every command here must stay silent-safe: the in-app updater runs this
; installer without UI. External binaries are invoked by absolute path — the
; installer inherits the invoking user's PATH, which elevation must not trust.

!macro TenebraServiceFailure step
  DetailPrint "Tenebra service ${step} failed (code $0)."
  MessageBox MB_ICONSTOP|MB_OK "Tenebra could not ${step} its Windows service (code $0). Installation needs repair.$\r$\nRerun this installer as administrator. Check %ProgramData%\Tenebra\service.log and Windows Event Viewer. Existing profiles are preserved." /SD IDOK
  SetErrorLevel 1
  Abort
!macroend

!macro TenebraRequireSuccess step
  ${If} $0 != 0
    !insertmacro TenebraServiceFailure "${step}"
  ${EndIf}
!macroend

!include "${__FILEDIR__}\installer-wfp-probe.nsh"
!include "${__FILEDIR__}\installer-release-protection.nsh"

!macro TenebraStopService
  ; 1060 means first install. Other query failures (including denied access)
  ; must stop installation before replacing a live service's files.
  nsExec::Exec /TIMEOUT=35000 '"$SYSDIR\sc.exe" query tenebra'
  Pop $0
  ${If} $0 == "0"
    nsExec::Exec /TIMEOUT=35000 '"$SYSDIR\sc.exe" stop tenebra'
    Pop $0
    ${If} $0 != 1062
      !insertmacro TenebraRequireSuccess "stop"
    ${EndIf}
    ; sc stop is asynchronous. WaitForStatus uses SCM's numeric state and is
    ; independent of the localized sc.exe output. No PATH or profile scripts.
    nsExec::Exec /TIMEOUT=35000 `"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -Command "try { (New-Object System.ServiceProcess.ServiceController('tenebra')).WaitForStatus([System.ServiceProcess.ServiceControllerStatus]::Stopped,[TimeSpan]::FromSeconds(30)); exit 0 } catch { exit 1 }"`
    Pop $0
    !insertmacro TenebraRequireSuccess "wait for stopped state of"
  ${ElseIf} $0 != 1060
    !insertmacro TenebraServiceFailure "query"
  ${EndIf}
!macroend

!macro NSIS_HOOK_PREINSTALL
  !insertmacro TenebraStopService
  ; The stopped state can precede the process exit by a moment, and the file
  ; stays locked until then: probe the old binary with an append-mode open
  ; (a write-lock test) before letting the template overwrite it. Bounded so
  ; a wedged process cannot hang the installer; if the file is still locked
  ; after the budget, the copy step surfaces the failure.
  ${If} ${FileExists} "$INSTDIR\tenebra-core.exe"
    StrCpy $1 20
    ${Do}
      ClearErrors
      FileOpen $0 "$INSTDIR\tenebra-core.exe" a
      ${IfNot} ${Errors}
        FileClose $0
        ${Break}
      ${EndIf}
      Sleep 500
      IntOp $1 $1 - 1
    ${LoopUntil} $1 < 1
    ${If} $1 < 1
      StrCpy $0 "binary still locked"
      !insertmacro TenebraServiceFailure "replace files for"
    ${EndIf}
  ${EndIf}
!macroend

!macro NSIS_HOOK_POSTINSTALL
  ; --- migrate a per-user v0.2.x install (see the header) ---
  ; The old registration is recognised by the Publisher the v0.2.x installer
  ; wrote; SHCTX is HKLM here, so HKCU below is always the old scope. All
  ; per-user shell paths must be read with the user context, not the
  ; install-wide one this section runs under.
  ReadRegStr $0 HKCU "${UNINSTKEY}" "Publisher"
  ${If} $0 == "${MANUFACTURER}"
    SetShellVarContext current

    ; The old binary location, for the provenance checks below: the quoted
    ; InstallLocation the old installer recorded, with the standard per-user
    ; path as the fallback.
    ReadRegStr $R8 HKCU "${UNINSTKEY}" "InstallLocation"
    StrCpy $0 $R8 1
    ${If} $0 == '"'
      StrCpy $R8 $R8 "" 1
      StrCpy $R8 $R8 -1
    ${EndIf}
    ${If} $R8 == ""
      StrCpy $R8 "$LOCALAPPDATA\${PRODUCTNAME}"
    ${EndIf}

    ; The Apps & Features entry and the installer's own breadcrumbs. The old
    ; install directory itself is intentionally left on disk.
    DeleteRegKey HKCU "${UNINSTKEY}"
    DeleteRegKey HKCU "${MANUPRODUCTKEY}"
    DeleteRegKey /ifempty HKCU "${MANUKEY}"

    ; The per-user autostart entry points at the old binary; drop it rather
    ; than resurrect a stale copy at every logon. Re-enabling autostart in
    ; the new install recreates it against the new path.
    DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "${PRODUCTNAME}"

    ; The per-user tenebra:// handler would shadow the machine-wide one the
    ; template just wrote (HKCU classes take precedence over HKLM). Only
    ; remove it while it provably points at the old binary.
    ReadRegStr $0 HKCU "Software\Classes\tenebra\shell\open\command" ""
    ${If} $0 == '"$R8\${MAINBINARYNAME}.exe" "%1"'
      DeleteRegKey HKCU "Software\Classes\tenebra"
    ${EndIf}

    ; The old user-scope shortcuts, again only while they provably target the
    ; old binary — a user's own same-named shortcut survives.
    !insertmacro IsShortcutTarget "$SMPROGRAMS\${PRODUCTNAME}.lnk" "$R8\${MAINBINARYNAME}.exe"
    Pop $0
    ${If} $0 = 1
      !insertmacro UnpinShortcut "$SMPROGRAMS\${PRODUCTNAME}.lnk"
      Delete "$SMPROGRAMS\${PRODUCTNAME}.lnk"
    ${EndIf}
    !insertmacro IsShortcutTarget "$DESKTOP\${PRODUCTNAME}.lnk" "$R8\${MAINBINARYNAME}.exe"
    Pop $0
    ${If} $0 = 1
      !insertmacro UnpinShortcut "$DESKTOP\${PRODUCTNAME}.lnk"
      Delete "$DESKTOP\${PRODUCTNAME}.lnk"
    ${EndIf}

    SetShellVarContext all
  ${EndIf}

  ; --- register and start the service ---
  ; The name must stay "tenebra": it is the name svc.Run answers to in
  ; tenebra-core. Idempotent across updates: create is a benign failure when
  ; the service already exists, and config then re-points the registration at
  ; this install directory (healing a relocated install).
  ;
  ; The binPath quoting is load-bearing. sc.exe splits its command line with
  ; CommandLineToArgvW, so the image path must arrive as ONE argv element that
  ; still carries its own quotes (a quoted ImagePath is also what keeps the
  ; "Program Files" space from being interpreted at service start). On the raw
  ; command line that is  binPath= "\"...\""  — an outer quoted region with
  ; backslash-escaped inner quotes. Writing "$\"...$\"" (no backslashes) puts
  ; ""..."" on the wire, which CommandLineToArgvW splits at the path's space:
  ; sc then sees binPath= C:\Program and answers with its usage text (1639),
  ; silently, and the service never exists.
  nsExec::Exec /TIMEOUT=35000 '"$SYSDIR\sc.exe" create tenebra binPath= "\$\"$INSTDIR\tenebra-core.exe\$\"" start= auto DisplayName= "Tenebra"'
  Pop $0
  ${If} $0 != 1073
    !insertmacro TenebraRequireSuccess "register"
  ${EndIf}
  nsExec::Exec /TIMEOUT=35000 '"$SYSDIR\sc.exe" config tenebra binPath= "\$\"$INSTDIR\tenebra-core.exe\$\"" start= auto obj= LocalSystem DisplayName= "Tenebra"'
  Pop $0
  !insertmacro TenebraRequireSuccess "configure"
  nsExec::Exec /TIMEOUT=35000 '"$SYSDIR\sc.exe" description tenebra "Runs the Tenebra VPN tunnel and serves the local control endpoint."'
  Pop $0
  !insertmacro TenebraRequireSuccess "describe"
  nsExec::Exec /TIMEOUT=35000 '"$SYSDIR\sc.exe" start tenebra'
  Pop $0
  ${If} $0 != 1056
    !insertmacro TenebraRequireSuccess "start"
  ${EndIf}
  ; This executable has just been installed in the administrator-owned install
  ; directory. The helper runs BEFORE Tauri initialization: no window, sidecar,
  ; autostart, updater or imports. It authenticates SCM PID, LocalSystem account and registered image,
  ; requires RUNNING and a status response matching its compiled-in version.
  nsExec::Exec /TIMEOUT=35000 '"$INSTDIR\${MAINBINARYNAME}.exe" --service-check'
  Pop $0
  !insertmacro TenebraRequireSuccess "verify readiness of"
!macroend

!macro NSIS_HOOK_PREUNINSTALL
  ; The same checked stop applies before both update and real uninstall.
  ; Keep the registration through updates; POSTINSTALL reconfigures it.
  !insertmacro TenebraStopService
  ${If} $UpdateMode <> 1
    ; Legacy cores cannot create T05 policy and do not implement its remover.
    ; A read-only absence proof permits their uninstall without executing them.
    !insertmacro TenebraProbeHostProtection
    ${If} $0 == "present"
      !insertmacro TenebraReleaseHostProtection
      !insertmacro TenebraProbeHostProtection
      ${If} $0 != "absent"
        StrCpy $0 "owned WFP objects remain after cleanup"
        !insertmacro TenebraServiceFailure "confirm host protection removal before unregistering"
      ${EndIf}
    ${EndIf}
    nsExec::Exec /TIMEOUT=35000 '"$SYSDIR\sc.exe" delete tenebra'
    Pop $0
    ${If} $0 != 1060
      !insertmacro TenebraRequireSuccess "unregister"
    ${EndIf}
  ${EndIf}
!macroend
