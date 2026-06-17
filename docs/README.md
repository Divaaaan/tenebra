# Tenebra documentation

Project overview and quick-start live in the top-level
[README](../README.md). This folder holds the deeper references.

- **[architecture.md](architecture.md)** — the layers (Go core, platform
  adapters, the desktop UI) and how they connect, plus the project's hard rules.
- **[control-protocol.md](control-protocol.md)** — the line-delimited JSON
  protocol the desktop UI uses to drive the core: every request, response and
  event, and the shared types.
- **[development.md](development.md)** — the full set-up, build, run and test
  walkthrough, the environment variables, coding conventions and troubleshooting.

Also at the repository root:

- **[CONTRIBUTING.md](../CONTRIBUTING.md)** — how to get set up and propose a
  change.
- **[SECURITY.md](../SECURITY.md)** — how to report a vulnerability and the
  project's trust stance.
- **[CHANGELOG.md](../CHANGELOG.md)** — what's changed.

New to the codebase? Read [architecture.md](architecture.md) first for the mental
model, then [development.md](development.md) to get it building.
