# macOS release integration and native acceptance

This describes the integration and native acceptance required for coherent macOS package releases. Build artifacts remain candidates until their exact source and bytes pass the runtime and migration acceptance checks. This document does not replace the storage/runtime contracts.

## User-visible result

A user opens a standard installer once, then starts Tenebra from Applications as their ordinary account. launchd owns the privileged tunnel process. No root GUI, shell invocation, open Terminal window or parent shell is required to keep VPN running. App, core, engine and bundled rules are upgraded together. The GUI-only updater stays disabled until an integrated updater can maintain this invariant.

## Build contract

Use scripts/macos/build-distribution.sh and packaging/macos/BUILDING.md. Build only an exact clean source commit. Read version from the existing set-version-managed files and Go pin from.go-version; never relabel an old build0.6.2. Both local and CI paths produce a PKG, hashes and a provenance manifest. arm64 is the local default; universal requires actual arm64 and x86_64 slices for GUI/core/engine/checker. The package's macOS15.0 requirement must cover all binary minimums; a higher future floor requires an explicit packaging change.

The standalone macos-package workflow produces review artifacts with contents:read and no signing secrets or publisher side effects. The historical release/candidate workflows are not yet switched over. Before the accepted release, their macOS artifact selection and acceptance receipts must consume this coherent PKG instead of treating an app-only DMG/updater archive as a complete installation. Keep existing release serialization and exact-byte/native-acceptance gates; no tag, channel or public release is authorized by the preparation workflow.

## Runtime and migration boundary

- Preserve unprivileged GUI -> authenticated Unix socket -> root launchd service. GUI fallback sidecar alone cannot provide the privileged tunnel and is not the normal installed path.
- Preserve versioned status and explicit readiness/error reporting. A matching status response proves the process is answering; it does not prove operational commit, healthy TUN, DNS or leak protection.
- The runtime owns canonical profile/settings/reference migration, private-root authority, schema checkpoint and operational commit/fence. The PKG owns its managed executable/plist backup. These are separate scopes: do not copy/restore/delete private profile files from the package scripts as a substitute for C2/C3.
- Review actual C2/C3/fence/status changes before combining branches. Do not release the installer fence while a checkpoint restore is incomplete or ambiguous; do not start a half-migrated configuration because a socket exists. The exact new protocol is taken from the accepted CONTRACTS.md/implementation, not invented here.
- Proxy restoration must target the exact service/configuration that Tenebra acquired; preserve unrelated proxy settings, not whichever interface later became primary. Startup/reconnect/readiness, stale sockets, schema-aware rollback and crash boundaries remain required runtime fixes and acceptance inputs.
- New subscription privacy policy remains: blocked+kill switch without a working VPN waits; manual direct operation requires explicit one-operation consent. Packaging success does not establish this transport guarantee.

## Restoring prior absence during migration rollback

A rollback restores the complete checkpoint, including names that were absent before migration. Retain newly created bytes in a verified inert archive through the accepted private-storage API. A completed restore must not leave an unresolved recovery blocker; an uncertain result keeps the operational fence until its receipt, source and archive are reconciled. An absent source name alone does not prove completion.

Native APFS acceptance includes absent-before and preexisting-file cases, expected-byte mismatch, active lease, safe repeat, and crashes around move/durability/receipt. Installer tests and successful compilation do not substitute for this migration test.

## Native acceptance for the fixed candidate

Record exact source SHA, app/core/engine/checker hashes, OS/CPU and tool versions; store subscriptions/tokens only in protected test state.

1. Fresh install and upgrade from the existing older installation preserve profiles/settings and permissions. GUI launches as the user, daemon as root under launchd; all component versions/source identities agree. Observe actual protocol readiness and operational migration completion.
2. Open the installed app from Applications with no Terminal window. If a dedicated test Terminal was used, close only that window/session and verify service PID/liveness and VPN traffic are independent. Do not kill unrelated Terminal/SSH sessions. Distinguish closing the GUI window from Quit and apply the product's documented Quit behavior; neither may accidentally own daemon lifetime.
3. Connect a real test subscription/server; prove delivered traffic, routing, DNS and IPv4/IPv6 behavior, not only Connected text. Check conflict with other VPNs (the current Darwin route-conflict probe is absent). Preserve/restore the pre-test network state.
4. Disconnect/reconnect and GUI reopen do not lose configuration or leave stale proxy/routes. Restart only the test daemon/engine and confirm bounded recovery or explicit failure. With kill switch enabled, verify the declared egress policy during the failure/restart interval: a launchd restart alone does not prove fail-closed transport. Review crash stderr/diagnostics without exposing tokens.
5. Sleep/wake and actual reboot restore the documented state without Terminal/admin relaunch. Missing/denied service must give actionable UI state, never a false working-tunnel indication.
6. Failed upgrade/readiness restores prior executable payload and prior service registration; test data migration rollback under C3 independently, including absent-before files. An interruption between Installer callbacks leaves a protected recoverable backup and cannot be advertised as atomic.

These checks are pending on the fixed candidate. Apple Silicon can be accepted on this Mac; universal packaging does not itself prove native Intel behavior. Apple Developer ID/notarization is not provided by ad-hoc signing; keep that distribution limitation explicit.
