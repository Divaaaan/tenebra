# Windows host protection: candidate contract and acceptance

Status at the 2026-09-11 audit candidate: **implemented in source; native packet,
installer and reboot acceptance remains pending**. This document describes the
T05 candidate integrated at `7aff666`. It does not change the evidence for the
previous v0.5.11 release or imply that a published installer contains this guard.
Passing unit tests, a successful build or a green hosted CI run is not proof of
traffic blocking. Release approval requires the native gates below against the
exact candidate binaries.

**INC-01 remains open: cause unknown.** The reported Windows connection failure
has not been causally reproduced or explained by these fixes. Neither the guard
implementation nor a passing unrelated subscription proves that incident fixed.

## Implemented contract

The Windows amd64/arm64 backend installs persistent WFP objects in four ALE
authorization layers: connect and receive/accept, each for IPv4 and IPv6. Once
applied, its intended contract is to block ordinary host application traffic
outside verified allowed paths even if the engine or Tenebra service dies.
Ordinary service stop, daemon Close, update and exhausted engine retries retain
the policy. The engine supervisor assigns its suspended child to a job before
resuming it, so service death is intended to terminate the engine as well.

Allowed paths are genuine loopback, the exact verified TUN interface, trusted
core/engine executable identities plus execution SID, service-scoped DHCP and
minimal IPv6 NDP. Physical plaintext DNS is blocked above the application
exceptions. Core bootstrap uses certificate-verified DoH/DoT to an explicit
literal-IP endpoint; invalid settings or resolver failure have no silent
plaintext/OS-DNS fallback. Saved resolver settings are not silently replaced.
Engine transport, encrypted bootstrap and configured DIRECT routes carried by
the engine are deliberate exceptions; application/domain/LAN split choices do
not become general host firewall permits.

Before an engine replacement, the daemon applies lockdown without a TUN permit.
It adds the verified TUN only after the engine probe and publishes Active only
after all local gates, including system proxy, succeed. A replacement adapter
with the same name cannot inherit the old LUID permission. System-proxy mode
uses loopback plus engine egress and grants no TUN exception.

`kill_switch` is the desired setting. `protection` is separate evidence:

| Status | Meaning |
| --- | --- |
| `off` | No confirmed owned policy; the first idle ON arms the next connection and does not itself assert enforcement. |
| `applying` | An operation is pending; previous confirmed enforcement flags remain. |
| `blocked` | Confirmed policy without an accepted engine connection. |
| `active` | Confirmed policy plus an accepted engine connection. |
| `error` | Apply, inspection or cleanup failed; previous confirmed flags remain, and the error must be visible. |
| `unavailable` | No supported protection backend. |

`enforced` and `persistent` describe last-confirmed policy, not an independent
packet measurement. Service loss cannot justify displaying a live Active
connection. Startup inspects existing policy before autoconnect and repairs it
to lockdown even when preferences say OFF, because cleanup may have been
interrupted. IPC remains available for explicit recovery if that operation fails.

## Ownership and maintenance

The provider is `fcb43b44-9358-4cd7-a998-9e7f822d5248`; the sublayer is
`fcb43b45-9358-4cd7-a998-9e7f822d5248`. Both use the marker
`tenebra/persistent-host-guard/v1`. Replacement and removal validate ownership
and relationships and commit one transaction. Disabled owned objects remain
removable; they are not enforcement evidence. Foreign metadata or unresolved
references cause an error, not a broad firewall reset.

- **OFF / explicit Disconnect:** request owned cleanup and require confirmation.
  Saving OFF, closing the window, or stopping the service is insufficient.
  Failed cleanup stays visible and can be retried even if the saved preference
  already says OFF. A separate proxy-restore failure must also remain visible.
- **Update / same-version repair:** preserve the guard while the checked service
  stop and coordinated GUI/core replacement run. The replacement core recovers
  policy before autoconnect. Do not remove the guard as an update workaround.
- **Explicit uninstall:** after confirmed service stop, the installer probes both
  fixed WFP GUIDs. Confirmed absence allows legacy/pre-T05 uninstall without an
  unsupported CLI call. If either exists, the installed, trusted T05-capable
  `tenebra-core.exe --release-host-protection` must exit successfully and a
  second probe must confirm both absent before service/files are deleted.
  Missing/unsupported core, foreign ownership, uncertainty, timeout or cleanup
  failure aborts uninstall and preserves the recovery binary. Probe errors are
  not absence. Ordinary update mode never invokes this remover.
- **Rollback to pre-T05:** explicitly release and confirm owned cleanup with a
  T05-capable core before replacing it with an older binary. Keep a compatible
  remover in a protected installation until that succeeds. An old core cannot
  be expected to repair or remove the new policy. Never recover by deleting all
  firewall rules, deleting unknown WFP objects, or running an arbitrary
  user-writable executable elevated.

The remover does not initialize a daemon, require an engine, or read profiles.
Its zero exit means owned cleanup succeeded or no owned policy existed. The
installer bounds the cleanup child to 20 seconds and its wrapper to 35 seconds;
the read-only WFP probes and general WFP RPCs remain synchronous without an
overall caller-enforced deadline. See [delivery acceptance](delivery-acceptance.md)
for the separate service/installer/proxy gates.

## Boundaries that must stay explicit

This contract covers ordinary host IPv4/IPv6 application flows. It does not
claim protection before BFE initializes, while BFE is deliberately unavailable,
for forwarded Hyper-V/WSL/container traffic, against administrator/kernel
adversaries, or against competing hard-permit/callout behavior.

The implementation deliberately leaves the provider ServiceName unset, following
the SDK and [WFP object management](https://learn.microsoft.com/en-us/windows/win32/fwp/object-management).
The [provider reference](https://learn.microsoft.com/en-us/windows/win32/api/fwpmtypes/ns-fwpmtypes-fwpm_provider0)
has conflicting disabled-state wording. The implementation choice is settled;
post-BFE/reboot behavior still requires measured acceptance. Persistent filters
are not a separate boot-time packet policy.

Established-flow safety depends on ALE reauthorization and actual interface
conditions. If the packet gates show an escape after commit, reject this
candidate; a separately reviewed packet/callout design is required before that
promise can be made. Do not turn the observed escape into an undocumented grace
period. [Microsoft ALE reauthorization](https://learn.microsoft.com/en-us/windows/win32/fwp/ale-re-authorization).

## Isolated VM procedure

These are acceptance instructions, not an executed result or authorization to
run on a workstation. **Do not inspect, change or interact with host Hiddify.**
All service, process, route, registry, firewall and adapter mutations belong only
in the disposable guest. A packet observer must see the guest's isolated uplink
independently of the application; an in-app IP check alone is insufficient.

Use one Windows 11 VM, 4 GiB fixed RAM, two vCPUs, host CPU Maximum=50%, no
parallel builds and at most 15 minutes per acceptance phase. Provisioning/OOBE
is a separate phase. Use prebuilt artifacts; record their commit, versions and
SHA-256, guest Windows build, VM ID and guest BIOS UUID. The two IDs are distinct.
Before any native harness action require the expected hypervisor identity,
an affirmative invocation flag, and the provisioned guest marker
`C:\ProgramData\TenebraAcceptance\isolated-vm.json` with matching `schema=1`,
`vmId`, `guestUuid`, `vmName`, `runNonce`, `disposable=true`, and
`allowNativeAcceptance=true`. Guest checks must reject a workstation or ambiguous
identity. The host separately attests the allocation and CPU cap.

Take a clean checkpoint. Inventory owned WFP objects and unrelated firewall
policy; retain the trusted remover and console access. Establish working IPv4
and IPv6 external test endpoints before the run; lack of an IPv6 route means
the IPv6 gate is untested, not passed. Capture both families and UDP/TCP port 53
at the independent uplink. Tag test payloads and timestamp policy commit,
process/service exit, BFE readiness and GUI state. Correlate payload sequence
numbers generated after commit; label any pre-commit in-flight packets separately
instead of inventing a post-commit grace period. Bound probes to 10 packets/s,
64 KiB responses and hard deadlines. Use fresh plus preexisting outbound TCP,
UDP and QUIC flows and independently initiated inbound-accepted flows.

## Required packet and lifecycle gates

Every row is **pending native acceptance**. Record the exact triggering event,
packet capture interval, state/ownership evidence, expected outcome and result.

| Gate | Trigger and required observation |
| --- | --- |
| Initial lockdown | Start direct v4/v6 flows before ON, then connect to apply lockdown. From confirmed commit onward, their next payloads and new ordinary physical flows must not reach the uplink. Idle first ON alone is not a commit. |
| Accepted TUN | Verify the configured name/address/LUID, successful engine probe and local gates. Ordinary traffic must reach the remote endpoint through the tunnel; no ordinary direct physical payload. A failed local gate must never publish Active/Connected. |
| Inbound and reauthorization | Keep inbound-accepted and outbound flows alive across initial commit, reconnect and default-route/next-hop replacement. Replies and new packets must not escape to the physical path after the relevant policy change. |
| System proxy and DIRECT choices | Test mixed/system-proxy mode and app/domain/LAN DIRECT choices. Loopback clients may use the engine; an ordinary process attempting the same physical destination directly remains blocked. Record intended engine DIRECT traffic separately. |
| DNS and bootstrap | Attempt ordinary and trusted-core UDP/TCP port-53 DNS on the physical path; none may escape. Verify encrypted literal-IP bootstrap and successful hostname-server connection. Invalid/hostname/plaintext endpoints, bad certificates, redirects, outage and timeout must fail visibly without plaintext retry. |
| Engine / service death | Kill the engine, hard-kill the service, and separately request graceful service Stop. Record engine/job termination. Keep probes running: policy remains, no unintended physical payload, and UI loses live Active status. |
| Retry exhaustion / replacement | Trigger five immediate engine crashes and a normal reconnect. Lockdown persists after retry exhaustion and throughout replacement; only a newly verified accepted engine permits recovery. |
| TUN loss / route change | Remove the verified guest TUN, replace it with a same-name/different-LUID adapter, and introduce a new physical uplink/default route. No substitute gains the TUN permit; no direct escape occurs even before the watcher reacts. Failed engine Stop remains visible. |
| DHCP / NDP / resume | Renew v4/v6 leases, exercise IPv6 neighbor/router discovery, sleep/resume and change the guest uplink. Required configuration traffic works; arbitrary LAN payload and non-permitted ICMP/data stay blocked. Reconnect revalidates the TUN. |
| Update / repair gap | Run candidate upgrade and same-version repair with policy present. Capture the full checked-stop/replacement/start gap; policy persists and old executable exceptions are replaced on successful recovery. A failed update must not silently release protection. |
| BFE restart without Tenebra | With persistent policy installed and Tenebra kept stopped in the guest, restart BFE. Record the BFE-down interval separately as outside scope. After BFE is ready and before Tenebra restarts, confirm non-disabled persistent objects and zero unintended physical payload. Only then test core recovery. |
| Reboot without Tenebra | Keep Tenebra from autostarting for this guest-only case, retain policy and reboot. Capture before boot through BFE readiness and subsequent probes. Separate pre-BFE traffic from the gate: once BFE loads policy, blocking must work before any Tenebra process starts. Then start Tenebra and verify lockdown/reconnect recovery. |
| Failed apply / commit | Inject a controlled apply or commit failure. Compare owned inventory and packets: previous committed policy remains, no partial permit set or transient direct payload, no false Active. Include disabled owned provider/filter recovery and refusal of foreign ownership. |
| Identity rejection | Use guest fixtures with a user-writable executable path, reparse traversal, wrong execution identity or colliding TUN name. Protection must reject uncertain identity without widening old policy. Restore the checkpoint after malicious fixtures. |
| OFF / Disconnect / retry | Explicitly release and verify both owned GUIDs and filters absent, intended direct connectivity restored, and unrelated firewall inventory unchanged. Inject cleanup failure: preserve owned policy/error and retry successfully even with preference already OFF. Test proxy-restore errors separately. |
| Uninstall / legacy / rollback | Cover successful T05 cleanup, retained binary on failed cleanup, second-probe failure, legacy absence, and legacy/missing remover with objects present. Upgrade never clears policy. A pre-T05 rollback occurs only after confirmed cleanup with the compatible remover. |

Pass requires zero unintended physical payload and plaintext DNS in the covered
blocked intervals for both IP families, measured tunnel recovery when Active,
and unchanged unrelated firewall inventory after release. Missing captures,
untested address families, UI-only evidence, or inability to exercise BFE/reboot
leave the corresponding gate open. Revert the disposable checkpoint on failure;
never perform workstation cleanup as a substitute.
