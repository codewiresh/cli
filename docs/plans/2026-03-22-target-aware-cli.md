# Target-Aware CLI Architecture

Date: 2026-03-22

## Summary

Codewire should converge on a target-aware CLI where local execution and environment execution feel like the same product surface:

- `cw use <target>`
- `cw current`
- `cw run -- ...`
- `cw exec -- ...`
- `cw ssh`
- `cw list`

The important constraint is that the current backends are not actually identical today. Local `cw run` talks to a Codewire node/session transport. Environment operations mostly go through platform APIs (`exec`, lifecycle APIs) and SSH/WireGuard/WebSocket access.

The design should be honest about that split now, while creating a migration path toward true transport equivalence for first-party environments that include the Codewire node in the base image.

## Why This Matters

Today the CLI has two overlapping mental models:

- local/session-oriented commands: `cw run`, `cw attach`, `cw logs`, `cw wait`, `cw kill`
- environment-oriented commands: `cw env create`, `cw env exec`, `cw ssh`, `cw env logs`

This makes the product more powerful than it feels. Users have to decide whether they are working with:

- a node
- an environment
- a run inside an environment

Instead, the default mental model should be:

- choose a target
- run work there
- inspect or manage the target when needed

## Current Backend Reality

There are currently two different execution backends in the CLI:

### 1. Local node / session transport

`cw run` and the rest of the session commands talk to the Codewire node protocol over:

- local unix socket (`codewire.sock`)
- or remote websocket server targets

This backend supports rich managed session semantics:

- launch
- list
- resolve by name/tag/ID
- attach
- logs / watch
- kill

### 2. Environment transport

Environment commands primarily use platform APIs and shell access:

- `CreateEnvironment`, `ListEnvironments`, `GetEnvironment`
- `ExecInEnvironment`
- `cw ssh` via WireGuard or websocket proxy

This backend can run commands and open shells, but it is not yet the same managed session transport as the local node.

## Desired End State

The desired architecture is:

- local target = local Codewire node
- first-party environment target = remote Codewire node inside the environment
- arbitrary-image environment target = reduced-capability exec/shell target

That gives us a clean capability ladder:

### Local node target

Capabilities:

- `run`
- `exec`
- `list-runs`
- `logs`
- `attach`
- `kill`
- `watch`
- `resolve-run`

### First-party environment target with Codewire node

Capabilities:

- same as local node, via a remote-node transport
- optional `ssh` for interactive shell access

### Arbitrary-image environment target

Capabilities:

- `exec`
- `ssh`
- environment lifecycle

Possibly later:

- partial `run` compatibility via a proxy bridge

## Honest Now, Symmetric Later

The CLI should not fake symmetry too early.

What that means:

- do not silently pretend that environment `run`, `attach`, and `logs` already use the same transport as local runs
- do not make default commands slow by shelling into every environment to approximate session state
- do not overfit the user interface to implementation details that will change

Instead:

- model the CLI around the future target abstraction
- surface current capability differences clearly
- keep the first implementation slices fast and explicit
- migrate backend behavior under a stable user-facing model

## Target Model

Introduce a first-class execution target abstraction in the CLI.

Proposed kinds:

- `local`
- `env`

Potential future kinds:

- `server`
- `resource`

Persist the current target in platform config so context can survive across shells.

Proposed persisted shape:

- `current_target.kind`
- `current_target.ref`
- optional display metadata such as `name`

The stored reference for an environment should be its stable full environment ID, not just the short ID.

## CLI Model

### Primary daily-work commands

- `cw use <target>`
- `cw current`
- `cw run -- ...`
- `cw exec -- ...`
- `cw ssh`
- `cw list`
- `cw logs`
- `cw attach`

### Management commands

- `cw env list`
- `cw env create`
- `cw env info`
- `cw env stop`
- `cw env start`
- `cw env rm`

### Advanced/system commands

- `cw node ...`
- `cw relay ...`
- `cw server ...`
- `cw completion ...`
- `cw update`

## Command Semantics

### `cw use <target>`

Sets the current execution target.

Examples:

- `cw use local`
- `cw use env-fb08`
- `cw use f062947a`

### `cw current`

Shows the current execution target and, later, target capabilities.

Examples:

- `current: local`
- `current: env-fb08 (f062947a)`
- `capabilities: run exec logs attach ssh`

### `cw run`

Starts a managed Codewire workload on the current target.

Important:

- local target: native now
- env target: only native once the environment exposes a remote Codewire node

### `cw exec`

Runs a one-shot command on the current target.

This should become the preferred top-level primitive instead of burying raw command execution under `cw env exec`.

### `cw ssh`

Opens an interactive shell on the current target when supported.

## `cw list` Behavior

The current `cw list` is slow because it does per-environment remote inspection to discover runs:

- list environments via platform API
- for each running sandbox, call `ExecInEnvironment`
- inside the env, run `cw list --local --json`

This has two problems:

- latency grows linearly with the number of environments
- it fails on arbitrary images that do not contain `cw`

### Proposed behavior

- `cw list`
  - fast default
  - list environments only
  - do not inspect in-environment runs by default

- `cw list --runs` / `cw list -r`
  - explicitly inspect runs inside environments
  - use concurrent lookups to reduce tail latency
  - surface unavailable status cleanly for envs without `cw`

This preserves a fast top-level view while keeping richer inspection available when requested.

## Migration Phases

### Phase 1: Context and fast list

Ship now:

- `cw use`
- `cw current`
- persisted target context
- `cw list` fast by default
- `cw list --runs`

Do not yet claim that `cw run` is fully target-aware for environments.

### Phase 2: Top-level execution model

Add target-aware top-level execution without removing old commands:

- `cw exec` on current target
- `cw logs` / `cw ssh` target-aware where possible
- keep `cw env exec` as a compatibility path

### Phase 3: Remote node in first-party environments

For base images that include the Codewire node:

- discover remote-node availability
- expose a remote-node target implementation
- make `cw run`, `cw list --runs`, `cw logs`, `cw attach`, and `cw kill` use the same session semantics as local

### Phase 4: Capability-aware UX

Show capability differences clearly:

- `current: env-fb08 (remote node)`
- `current: python-slim (exec/ssh only)`

At that point, local and first-party environment targets become truly equivalent for most daily work.

## Why Remote Node in the Base Image Is the Key

If the base image includes the Codewire node, then first-party environments become remote nodes rather than just remote shells.

That means:

- `cw run` can become real session launch, not a shell-based approximation
- `cw list --runs` can talk to a remote node transport directly
- `cw logs` / `attach` / `kill` can work the same way as local

Without that, environment parity requires fragile command proxying over `exec` or `ssh`.

## Non-Goals for the First Slice

The first slice should not:

- make `cw run` appear fully environment-native before the backend exists
- remove `cw env exec`
- remove `cw node` or `cw relay`
- force expensive run inspection into default `cw list`

## First Implementation Slice

This document accompanies the first implementation slice:

- add persisted current-target context (`cw use`, `cw current`)
- make `cw list` fast by default
- add explicit `cw list --runs` / `-r`

The next slice after that should wire target-aware top-level `exec` and start introducing the target abstraction beneath command handlers.
