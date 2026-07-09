# Security policy

Tenebra is a VPN client, so security and privacy are the product, not a feature.
This document explains how to report a vulnerability and what trust stance the
project commits to.

> **Status:** Tenebra is in early development and has not been independently
> audited. The real tunnel path is still being validated. Don't rely on it to
> protect you in a high-risk situation yet.

## Reporting a vulnerability

**Please do not open a public issue, pull request, or discussion for a security
problem**, and don't disclose it publicly until it has been fixed and a release
is available.

Report privately through GitHub's coordinated-disclosure flow:

1. Go to the repository's **Security** tab.
2. Choose **Report a vulnerability** (this opens a private security advisory).
3. Describe the issue with enough detail to reproduce it — see below.

A private advisory keeps the report confidential between you and the maintainers
while it's being worked on. If you can't use GitHub advisories for some reason,
open a minimal issue that says only "security report — please provide a private
channel" with **no details**, and we'll arrange one.

### What to include

- What the vulnerability is and the impact you think it has.
- Step-by-step reproduction, and a proof of concept if you have one.
- Affected version / commit, OS, and build (e.g. the bundled installer vs. a
  local `tauri dev` build).
- Any suggested fix or mitigation.

Please use only test data — **no real servers, subscription URLs, or keys** — in
your report.

### What to expect

This is a small, volunteer-run project, so we can't promise enterprise SLAs, but
the intent is:

- acknowledge your report within a few days;
- keep you updated as we investigate and fix;
- credit you in the advisory and changelog when the fix ships, if you'd like
  (and respect a request to stay anonymous).

We welcome coordinated disclosure and will work with you on timing. Please give us
a reasonable window to fix an issue before any public write-up.

## Scope

**In scope** — anything in this repository that could weaken the security or
privacy of a user running Tenebra:

- the Go core (`core/`) — subscription/link parsing, profile storage, routing and
  config generation, the fallback logic, and the control protocol;
- the platform adapter(s) (`adapters/`) and the `tenebra-core` sidecar;
- the desktop shell and front end (`ui-desktop/`) and the core ↔ UI boundary;
- the build/resource scripts (`scripts/`) insofar as they affect what gets
  shipped.

Examples of in-scope issues: a parser that can be made to exfiltrate or mishandle
credentials; a traffic or DNS **leak** (traffic escaping the tunnel when it
shouldn't); the kill-switch failing to contain traffic; the leak check reporting a
false "safe"; local privilege escalation via the sidecar; the control channel
being driven by something other than the intended UI; secrets being written to
disk or logs in the clear.

**Out of scope:**

- Vulnerabilities in **third-party components** Tenebra builds on — report those
  upstream:
  - [sing-box](https://github.com/SagerNet/sing-box) (the tunnel engine),
  - [Tauri](https://github.com/tauri-apps/tauri), wintun, and other dependencies.
  We'll happily pull in a fixed upstream release once it exists.
- Issues that require an attacker who already has administrator/root on the
  machine, or physical access (running a VPN client already implies trusting the
  local OS).
- The **servers you connect to.** Tenebra ships no servers and runs no
  infrastructure; the trustworthiness of a subscription or node is between you and
  its operator.
- Theoretical reports with no realistic attack path, or output from automated
  scanners without a demonstrated, exploitable impact.

If you're unsure whether something is in scope, report it — we'd rather hear it.

## Our trust stance

For a VPN, what the software *doesn't* do matters as much as what it does. Tenebra
commits to:

- **No telemetry.** None. The client does not phone home, and there are no
  analytics or accounts. (See the hard rules in
  [docs/architecture.md](docs/architecture.md#hard-rules).)
- **No bundled servers or infrastructure.** There are no hardcoded servers,
  subscription URLs, node IPs or keys anywhere in the repo or the build. You bring
  your own subscription; your server list never leaves your machine except to the
  servers you chose.
- **Open source under GPLv3.** The whole client is auditable, and the copyleft
  license means shipped builds stay open. You don't have to take our word for any
  of the above — read the code.
- **Local-only secrets.** Profiles and subscription URLs are stored in your
  per-user config directory and are kept out of logs.
- **Leak-awareness by design.** Routing supports a kill-switch (drop proxied
  traffic rather than let it leak when the tunnel is down) and a LAN bypass, and
  the built-in leak check is deliberately *honest* — it reports what it could not
  verify instead of claiming "safe". These are protective intents, not audited
  guarantees, and are part of what's still being validated.

These properties are invariants. If you find a case where the software violates
one of them, that is itself a security issue worth reporting.

## Update signing key

The in-app updater installs an update only when its signature verifies against a
[minisign](https://jedisct1.github.io/minisign/) public key compiled into every
build (see `ui-desktop/src-tauri/tauri.conf.json`). That key is the trust anchor
for updates, so its custody matters:

- **Custody.** The private half of the key is held only as an encrypted secret in
  the release CI, available to the maintainer(s) who cut releases. It is never
  committed, and releases are signed by the CI rather than on a personal machine.
- **Rotation.** Because the *public* key is baked into shipped binaries, a new key
  takes effect only for clients that install a build carrying it. Existing clients
  accept updates signed by the key they already embed, so the key cannot be rotated
  through the in-app updater alone: rotation means publishing a build with the new
  public key that users install manually (a fresh download), after which signed
  in-app updates resume under the new key. Plan a rotation as a manually-installed
  release, not a normal update.
- **If the private key leaks.** Treat every update the exposed key could sign as
  untrusted. The response is to generate a new key pair, ship a manually-installed
  build carrying the new public key, and announce it through the repository's
  release notes and a security advisory so users re-install from a trusted source
  instead of accepting an in-app update. The compromised key is retired by
  superseding it: once users move to the new build, updates signed by the old key
  are no longer accepted.

## A note on maturity

Tenebra has not had a third-party security review, and the live tunnel still needs
validation. We're documenting that openly rather than overstating the guarantees.
As the project matures we expect to revisit this policy — contributions toward
hardening, fuzzing the parsers, and validating the leak/kill-switch behaviour are
all very welcome.
