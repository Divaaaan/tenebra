; Read-only probe of the fixed provider/sublayer identities. It neither inspects
; nor removes foreign policy and never launches an installed executable.
; $0 = absent only when BOTH exact NOT_FOUND results prove absence, else present.
; Any other result aborts. Keep p handles/pointer-to-pointer outputs pointer-sized
; because the NSIS uninstaller is 32-bit even for the x64 bundle.
!macro TenebraProbeHostProtection
  Push $1
  Push $2
  Push $3
  Push $4
  Push $5
  StrCpy $1 0
  StrCpy $0 "WFP API unavailable"
  System::Call '"$SYSDIR\fwpuclnt.dll"::FwpmEngineOpen0(p 0, i 10, p 0, p 0, *p.r1) i.r0'
  ${If} $0 == "0"
  ${AndIf} $1 != "0"
    StrCpy $2 0
    StrCpy $4 "WFP provider API unavailable"
    System::Call '"$SYSDIR\fwpuclnt.dll"::FwpmProviderGetByKey0(p r1, g "{fcb43b44-9358-4cd7-a998-9e7f822d5248}", *p.r2) i.r4'
    ${If} $2 != "0"
      System::Call '"$SYSDIR\fwpuclnt.dll"::FwpmFreeMemory0(*p r2) v'
    ${EndIf}
    StrCpy $2 0
    StrCpy $5 "WFP sublayer API unavailable"
    System::Call '"$SYSDIR\fwpuclnt.dll"::FwpmSubLayerGetByKey0(p r1, g "{fcb43b45-9358-4cd7-a998-9e7f822d5248}", *p.r2) i.r5'
    ${If} $2 != "0"
      System::Call '"$SYSDIR\fwpuclnt.dll"::FwpmFreeMemory0(*p r2) v'
    ${EndIf}
    StrCpy $3 "WFP close API unavailable"
    System::Call '"$SYSDIR\fwpuclnt.dll"::FwpmEngineClose0(p r1) i.r3'
    ${If} $3 != "0"
      StrCpy $0 $3
    ${ElseIf} $4 == "-2144206843"
    ${AndIf} $5 == "-2144206841"
      ; FWP_E_PROVIDER_NOT_FOUND 0x80320005 / SUBLAYER_NOT_FOUND 0x80320007.
      StrCpy $0 "absent"
    ${Else}
      ${If} $4 != "0"
      ${AndIf} $4 != "-2144206843"
        StrCpy $0 $4
      ${ElseIf} $5 != "0"
      ${AndIf} $5 != "-2144206841"
        StrCpy $0 $5
      ${Else}
        StrCpy $0 "present"
      ${EndIf}
    ${EndIf}
  ${ElseIf} $0 == "0"
    StrCpy $0 "WFP returned no engine handle"
  ${EndIf}
  Pop $5
  Pop $4
  Pop $3
  Pop $2
  Pop $1
  ${If} $0 != "absent"
  ${AndIf} $0 != "present"
    !insertmacro TenebraServiceFailure "query owned host protection before unregistering"
  ${EndIf}
!macroend
