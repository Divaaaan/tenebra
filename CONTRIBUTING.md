# Contributing to Tenebra

Thanks for considering a contribution. Tenebra is a young, open-source VPN client
and there's plenty of high-value work to do — especially bringing up the real
tunnel and adding the non-Windows adapters. This guide gets you set up and
explains how we work.

By contributing you agree your work is licensed under the project's license,
[GPLv3](LICENSE).

## Ways to help

- **Code** — close one of the gaps below, fix a bug, or improve a rough edge.
- **Testing on real hardware** — the tunnel path needs elevated, real-server runs
  on actual machines; reporting exactly what happens is genuinely useful.
- **Docs** — if something here or in [docs/](docs/) was wrong or unclear, a fix is
  very welcome.
- **Triage** — reproducing issues and narrowing them down.

Highest-leverage areas right now (see
[the status table](README.md#project-status)):

1. **Live tunnel bring-up** on Windows (wintun + sing-box, elevated).
2. **New platform adapters** — macOS/Linux (utun), Android (`VpnService`),
   iOS (Network Extension). The Go core is already platform-agnostic.
3. **Release packaging** — the NSIS installer is unsigned and there's no release
   pipeline yet.

## Before you start

- For anything beyond a small fix, **open an issue first** so we can agree on the
  approach — it saves rework.
- Please **don't** report security vulnerabilities in public issues. Follow
  [SECURITY.md](SECURITY.md) instead.
- Skim [docs/architecture.md](docs/architecture.md) so a change lands in the right
  layer, and the **hard rules** below.

## Get set up

Prerequisites — **Go 1.24+**, **Node 22+**, the **Rust** toolchain — and the full
build/run/test walkthrough live in **[docs/development.md](docs/development.md)**.
The short version:

```
# core: build, vet, test (stdlib-only, runs offline)
go build ./...
go vet ./...
go test ./...

# desktop app (from ui-desktop/)
powershell -File scripts/fetch-resources.ps1            # fetch sing-box + wintun
go build -o ui-desktop/src-tauri/binaries/tenebra-core-x86_64-pc-windows-msvc.exe ./cmd/tenebra-core
cd ui-desktop && npm install && npm run tauri dev
```

For UI-only work you don't need the Go build at all — set `TENEBRA_MOCK=1` and the
shell serves a mock backend. Details and troubleshooting are in the dev guide.

## Repository layout

A quick map (full version in
[docs/development.md](docs/development.md#repository-layout)):

- `core/` — Go, platform-agnostic, **standard library only**, fully unit-tested.
  `model`, `subscription`, `profile`, `routing`, `singbox`, `fallback`, `control`.
- `adapters/` — the per-OS system tunnel. Only `windows` exists today.
- `cmd/tenebra-core/` — the sidecar binary; talks the protocol on stdin/stdout.
- `ui-desktop/` — Tauri 2: `src-tauri/` (Rust shell) + `src/` (React + TypeScript).
- `docs/` — architecture, the control protocol, and the dev guide.

The core never contains infrastructure: no hardcoded servers, subscription URLs,
node IPs or keys. Servers come from user input; tests use obviously fake data.

## Running the tests

CI runs the Go tests, the front-end tests and a Windows desktop build on every
push and pull request; keep them green. Locally:

```
go test ./...                       # Go unit tests (core/ and adapters/)
cd ui-desktop && npm test           # front-end unit tests (vitest)
cd ui-desktop && npm run typecheck  # front-end type-check
cd ui-desktop/src-tauri && cargo test   # Rust unit tests + the real-binary e2e
```

Notes:

- To mirror CI's Go run exactly: `go test ./... -race -count=1`, plus
  `staticcheck ./...`.
- The Rust **e2e** (`src-tauri/tests/sidecar_e2e.rs`) spawns the real
  `tenebra-core` and round-trips the wire protocol. It **self-skips** until you've
  built the sidecar into `src-tauri/binaries/`, so `cargo test` is green on a
  fresh checkout. Build the sidecar to make it run for real.
- The Go tests and the e2e never start an actual tunnel — that still needs a
  manual, elevated run.

See [docs/development.md](docs/development.md#running-the-tests) for the full
explanation of each surface.

## Coding conventions

- **English** for code, comments, identifiers, commits and docs.
- **Go**
  - Keep the core **stdlib-only** — do not add module dependencies to it. If a
    task seems to need one, raise it in an issue first.
  - `gofmt`/`go fmt` everything; keep `go vet ./...` and `staticcheck ./...`
    clean.
  - Prefer small, pure, table-tested functions — the core's testability is a
    feature. Network I/O stays at the edges (e.g. `subscription.Fetch`), so logic
    can be tested offline.
- **TypeScript / front end**
  - Keep `npm run typecheck` and `npm test` (vitest) clean.
  - User-facing strings go in `ui-desktop/src/i18n/strings.ts`, in **both**
    English and Russian (the shape is compile-checked).
  - Style through the **design tokens** in `ui-desktop/src/styles/tokens.css`
    (CSS custom properties for colour, spacing, radii, motion). Don't hard-code
    hex colours or magic pixel values; dark is the default and light flips via
    `[data-theme="light"]`.
- **The protocol is a contract.** If you change the control protocol, update all
  three sides together — the Go `core/control` types, the docs in
  [docs/control-protocol.md](docs/control-protocol.md), and the TypeScript mirror
  in `ui-desktop/src/api/types.ts` — and extend the e2e if you add a command.

## Hard rules (non-negotiable)

These are the project's invariants. A change that breaks one won't be merged.

- **GPLv3.** sing-box is GPLv3, so Tenebra is too. New files keep the license.
- **No telemetry.** None. This is a VPN; trust is the product.
- **No infrastructure in the repo.** No hardcoded servers, subscription URLs,
  node IPs or keys, ever — in code, tests, docs or fixtures.
- **No real secrets in tests or examples.** Use obviously fake hosts
  (`example.com`, RFC-5737 ranges) and placeholder keys.

More context for these is in
[docs/architecture.md](docs/architecture.md#hard-rules).

## Proposing a change

1. **Fork** the repo and create a branch off `main`. Use a short, descriptive
   name, e.g. `fix/subscription-base64-padding` or `feat/macos-adapter`.
2. **Make focused commits** with clear, imperative messages
   ("Add Salamander obfs parsing", not "fixes"). Keep unrelated changes out.
3. **Run the checks** that apply to your change (see above) and make sure CI would
   pass.
4. **Open a pull request** against `main`. Describe what changed and why, link the
   issue it addresses, and note anything you couldn't verify (e.g. "tunnel not
   tested live — no admin box available"). Honesty about what's unverified is
   expected, not penalized.
5. Be ready for review back-and-forth. Small, well-scoped PRs merge fastest.

## A note on honesty

Tenebra documents itself honestly — including what doesn't work yet. Please hold
contributions to the same bar: don't claim a feature is done when the happy path
isn't exercised, and flag assumptions rather than papering over them. It keeps the
project trustworthy, which for a VPN is the whole point.

Welcome aboard.
