---
name: codewire
description: Use the Codewire CLI to run, monitor, and coordinate persistent sessions, platform environments, and relay-backed workflows. Use when the task is about operating `cw`, not changing its implementation.
---

# Codewire CLI

## Purpose

Use `cw` effectively for session orchestration, environment access, secrets, platform operations, and relay-backed coordination.

## When to use

Use this skill when:
- the user wants help using `cw`
- the task is to launch or manage long-running sessions
- the task involves Codewire environments, SSH, secrets, or relay operations

## Workflow

1. Identify the command family.
   Distinguish between local session orchestration (`run`, `attach`, `watch`, `send`, `wait`, `kv`), environment management (`env`, `ssh`, `config-ssh`), and platform or org operations (`login`, `whoami`, `resources`, `secrets`, `template`).

2. Prefer current commands, not legacy aliases.
   Use `cw run` instead of older `launch` wording, and confirm the exact subcommand from the current CLI before giving examples.

3. Give working examples.
   Show the smallest useful command first, then add the common flags that matter for that task.

4. Call out stateful or destructive operations.
   Be explicit before `kill`, `env rm`, `env nuke`, secret deletion, or server mutations.

## Command groups

- Sessions:
  - `cw run`
  - `cw attach`
  - `cw logs`
  - `cw send`
  - `cw watch`
  - `cw status`
  - `cw kill`
  - `cw subscribe`
  - `cw wait`
  - `cw inbox`

- Node and coordination:
  - `cw node`
  - `cw nodes`
  - `cw kv`
  - `cw msg`
  - `cw listen`
  - `cw request`
  - `cw reply`

- Environment and access:
  - `cw env create|list|info|start|stop|logs|exec|cp|rm|prune|nuke`
  - `cw ssh`
  - `cw config-ssh`

- Platform and admin:
  - `cw login`
  - `cw logout`
  - `cw whoami`
  - `cw resources`
  - `cw secrets`
  - `cw template`
  - `cw billing`

- Relay and setup:
  - `cw relay`
  - `cw relay-setup`
  - `cw setup`
  - `cw gateway`
  - `cw hook`

## Decision rules

- If you are unsure about flags or subcommands, check the current Cobra definitions before answering.
- Prefer examples that match the user’s actual goal instead of dumping the entire command surface.
- For session orchestration, name the target, tags, and working directory explicitly when they matter.

## Output expectations

- Give current command examples, not stale ones.
- If the command can mutate or destroy state, say that directly.
