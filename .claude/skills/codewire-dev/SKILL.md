---
name: codewire-dev
description: Work on the Codewire CLI and backend codebase. Use when implementing, debugging, or testing `cw`, its session engine, platform commands, or relay features.
---

# Codewire Development

## Purpose

Work effectively inside the `codewire-cli` codebase without relying on stale command or file-layout assumptions.

## When to use

Use this skill when:
- implementing or debugging Codewire CLI features
- changing session orchestration, protocol, relay, or platform behavior
- adding tests for `cw` behavior

## Workflow

1. Orient on the current command surface.
   Check `cmd/cw/*.go` before assuming a command name or flag still exists.

2. Map the change to the correct subsystem.
   Use the file map below to find the real owner of the behavior.

3. Update implementation and tests together.
   Treat command behavior, protocol behavior, and session lifecycle changes as test-bearing changes.

4. Verify the right layer.
   Run the narrowest useful test target first, then broader integration coverage when the change crosses session, relay, or CLI boundaries.

## Key file map

- CLI command definitions:
  - `cmd/cw/main.go`
  - `cmd/cw/env_cmd.go`
  - `cmd/cw/env_exec_cmd.go`
  - `cmd/cw/platform*.go`
  - `cmd/cw/ssh_cmd.go`
  - `cmd/cw/secrets.go`

- Client and protocol:
  - `internal/client/`
  - `internal/protocol/`
  - `internal/connection/`

- Session engine and terminal behavior:
  - `internal/session/`
  - `internal/terminal/`
  - `internal/node/`

- Relay and platform flows:
  - `internal/relay/`
  - `internal/platform/`
  - `internal/auth/`
  - `internal/oauth/`
  - `internal/store/`

- MCP and supporting services:
  - `internal/mcp/`
  - `internal/statusbar/`
  - `internal/update/`

- Tests:
  - `tests/integration_test.go`
  - `tests/events_test.go`
  - `tests/messaging_test.go`
  - `tests/relay_*`

## Verification

- Build or targeted test:
  - `make build`
  - `make test`
  - `go test ./...`

- Use integration coverage when behavior crosses process boundaries, relay flows, or CLI UX.

## Decision rules

- Do not trust old `launch`-era documentation when changing command behavior; the current CLI is defined in `cmd/cw/*.go`.
- Prefer fixing the real protocol or session owner instead of layering CLI-only workarounds.
- Keep examples and tests aligned with the actual current command names.

## Output expectations

- Name the subsystem you changed.
- State what verification ran and what broader integration coverage is still needed, if any.
