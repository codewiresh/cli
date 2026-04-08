# Task Reporting Design: JetStream-Backed Relay

**Goal:** Let LLM agent sessions report current task state through MCP, retain a durable ordered task event stream at the relay tier, and expose both latest-state snapshots and live/replayable watches to `cw tasks`.

**Decision:** Use NATS JetStream as the relay-side event backbone. Keep node-local session event logs for local observability, but move relay aggregation from in-memory-only state to JetStream stream + KV buckets.

**Why this design:**
- The relay is expected to become distributed.
- We want replay, durability, and cross-instance consistency.
- We do not want node agents to hold NATS credentials or bypass relay auth.
- We still want the simple local-only path to work when relay connectivity is absent.

**Non-goals for v1:**
- No direct node -> NATS publish path.
- No platform-side task persistence outside the relay tier.
- No MCP session auto-detection.
- No mandatory speech backend.

---

## Architecture

```text
Agent session
  -> MCP tool: codewire_report_task(session_id, summary, state)
    -> node request: ReportTask
      -> SessionManager.ReportTask(...)
        -> session event log
        -> local subscriptions
        -> optional relay forward callback
          -> node agent websocket
            -> relay edge instance
              -> authenticate node
              -> enrich with network_id + node_name
              -> publish to JetStream stream TASK_EVENTS
              -> update JetStream KV bucket TASK_LATEST
                -> GET /api/v1/tasks
                -> GET /api/v1/tasks/events
                  -> cw tasks [--watch] [--speak]
```

---

## Core Design Decisions

### 1. Relay instances stay on the auth boundary

Nodes continue talking only to relay instances over the existing authenticated websocket path.

Relay instances:
- authenticate node tokens
- enrich reports with `network_id` and `node_name`
- publish normalized task events to JetStream
- serve snapshot and SSE APIs to users

This keeps NATS private to the relay tier and avoids distributing NATS credentials or authorization logic to nodes.

### 2. Use both a stream and a KV bucket

Use JetStream for two distinct concerns:

- `TASK_EVENTS` stream
  - append-only, ordered, replayable event log
  - backing store for watches and reconnects

- `TASK_LATEST` KV bucket
  - latest task state per `(network, node, session)`
  - backing store for `cw tasks` snapshot/list

The stream solves replay and ordering. The KV bucket solves efficient latest-state queries.

### 3. SSE cursor is the JetStream stream sequence

`Last-Event-ID` in the CLI maps to the JetStream stream sequence, not to a relay-local counter.

That gives a stable distributed cursor independent of which relay instance serves the request.

### 4. Publish idempotently

Each task report gets a stable `event_id`.

Relay publishes to JetStream with:
- header `Nats-Msg-Id: <event_id>`

This gives duplicate suppression within JetStream's configured dedupe window.

### 5. Keep the local node path useful

Even when relay forwarding fails:
- the session event log still records `task.report`
- local node subscribers still receive the event

Distributed durability is additive, not a replacement for local observability.

---

## JetStream Layout

### Stream: `TASK_EVENTS`

**Purpose:** durable ordered event log

**Subjects:**

```text
tasks.<network_id>.<node_name>.s<session_id>
```

Example:

```text
tasks.project-alpha.dev-1.s42
```

**Retention shape:**
- limits-based retention
- file storage
- replicated in production
- bounded by max age and/or size

**Notes:**
- use `session_id`, not `session_name`, in the subject
- keep `session_name` in the payload
- avoid subject tokens that depend on free-form user text

### KV bucket: `TASK_LATEST`

**Purpose:** latest state per session

**Keys:**

```text
<network_id>.<node_name>.s<session_id>
```

Example:

```text
project-alpha.dev-1.s42
```

**Value:** JSON-encoded latest task state including the JetStream stream sequence that most recently updated the key.

---

## Data Model

### Local session event payload

```go
type TaskReportData struct {
	EventID string `json:"event_id"`
	Summary string `json:"summary"`
	State   string `json:"state"`
}
```

### Node -> relay websocket payload

```go
type TaskReportMessage struct {
	Type        string `json:"type"` // "TaskReport"
	EventID     string `json:"event_id"`
	SessionID   uint32 `json:"session_id"`
	SessionName string `json:"session_name,omitempty"`
	Summary     string `json:"summary"`
	State       string `json:"state"` // "working", "complete", "blocked", "failed"
	Timestamp   string `json:"timestamp"`
}
```

### JetStream event payload

```go
type TaskEvent struct {
	EventID     string `json:"event_id"`
	NetworkID   string `json:"network_id"`
	NodeName    string `json:"node_name"`
	SessionID   uint32 `json:"session_id"`
	SessionName string `json:"session_name,omitempty"`
	Summary     string `json:"summary"`
	State       string `json:"state"`
	Timestamp   string `json:"timestamp"`
}
```

### Snapshot value stored in `TASK_LATEST`

```go
type LatestTaskValue struct {
	EventID     string `json:"event_id"`
	StreamSeq   uint64 `json:"stream_seq"`
	NetworkID   string `json:"network_id"`
	NodeName    string `json:"node_name"`
	SessionID   uint32 `json:"session_id"`
	SessionName string `json:"session_name,omitempty"`
	Summary     string `json:"summary"`
	State       string `json:"state"`
	Timestamp   string `json:"timestamp"`
}
```

### API/SSE payload

```go
type TaskAPIEvent struct {
	Seq         uint64 `json:"seq"`
	Type        string `json:"type"` // "task.report" or "stream.reset"
	EventID     string `json:"event_id,omitempty"`
	NetworkID   string `json:"network_id"`
	NodeName    string `json:"node_name,omitempty"`
	SessionID   uint32 `json:"session_id,omitempty"`
	SessionName string `json:"session_name,omitempty"`
	Summary     string `json:"summary,omitempty"`
	State       string `json:"state,omitempty"`
	Timestamp   string `json:"timestamp,omitempty"`
}
```

---

## Flow Details

### Write path

1. Agent calls `codewire_report_task(session_id, summary, state)`.
2. Node handler calls `SessionManager.ReportTask(...)`.
3. `SessionManager.ReportTask(...)`:
   - normalizes and validates
   - creates `event_id`
   - appends `task.report` to the local session event log
   - publishes to local subscribers
   - invokes relay forward callback if configured
4. Node agent sends `TaskReportMessage` over the existing websocket.
5. Relay edge authenticates from the existing node websocket context.
6. Relay publishes to JetStream subject `tasks.<network>.<node>.s<id>` with `Nats-Msg-Id = event_id`.
7. Relay receives publish ack including stream sequence.
8. Relay writes/updates `TASK_LATEST` for the session using the acked stream sequence.

### Snapshot path

1. `cw tasks` calls relay `GET /api/v1/tasks?network_id=...`.
2. Relay checks user auth and membership.
3. Relay reads relevant `TASK_LATEST` keys.
4. Relay returns latest task per `(node, session)`.

### Watch path

1. `cw tasks --watch` calls `GET /api/v1/tasks/events?network_id=...`.
2. Relay checks user auth and membership.
3. Relay reads `Last-Event-ID`.
4. Relay compares it to the current stream window.
5. If the requested sequence is no longer available, relay emits:

```text
event: stream.reset
id: <first_available_seq>
```

6. Otherwise relay creates an ephemeral ordered stream reader starting after the requested sequence.
7. Relay bridges JetStream messages to SSE with:
   - `id: <stream_seq>`
   - `event: task.report`
   - `data: <TaskAPIEvent JSON>`

---

## API Surface

### `GET /api/v1/tasks`

**Purpose:** latest-state snapshot

**Auth:** user auth with network membership

**Query params:**
- `network_id` required
- `node` optional
- `session_id` optional
- `state` optional

### `GET /api/v1/tasks/events`

**Purpose:** live + replay watch stream

**Auth:** user auth with network membership

**Query params:**
- `network_id` required
- `node` optional
- `session_id` optional
- `state` optional

**Headers:**
- `Last-Event-ID` optional, containing JetStream stream sequence

---

## Code Structure

### Local node/session

- `internal/session/events.go`
  - add `EventTaskReport`
  - add `TaskReportData`
  - add constructor

- `internal/session/session.go`
  - add `SessionManager.ReportTask(...)`
  - add relay forward callback registration
  - generate stable `event_id`

- `internal/protocol/messages.go`
  - add `Summary`, `State`

- `internal/node/handler.go`
  - add `ReportTask`

- `internal/node/node.go`
  - wire relay forward callback into node agent outbound channel

### Relay edge and JetStream integration

- `internal/relay/agent.go`
  - outbound websocket writes from node to relay

- `internal/relay/node_handler.go`
  - parse inbound `TaskReportMessage`
  - enrich with authenticated node context
  - call JetStream-backed task store

- `internal/relay/task_store.go`
  - JetStream and KV integration
  - publish event
  - write latest-state KV
  - read snapshot
  - create watch readers

- `internal/relay/task_events.go`
  - HTTP handlers for snapshot and SSE

- `internal/relay/relay.go`
  - initialize NATS / JetStream clients
  - construct task store
  - register endpoints

### MCP and CLI

- `internal/mcp/server.go`
  - add `codewire_report_task`
  - `session_id` required in v1

- `internal/client/tasks.go`
  - relay auth loading
  - snapshot fetch
  - SSE watch
  - cursor persistence

- `internal/client/tasks_tts.go`
  - optional local speech

- `cmd/cw/tasks.go`
  - `cw tasks`
  - `cw tasks --watch`
  - `cw tasks --watch --speak`

---

## Validation Rules

Task reports should be normalized before persistence or relay publication:

- trim leading/trailing whitespace
- reject empty summaries after trimming
- enforce a max summary length
- validate state against:
  - `working`
  - `complete`
  - `blocked`
  - `failed`

Optional later refinement:
- suppress or coalesce duplicate consecutive reports for speech playback

---

## Cross-Platform Speech

Speech remains optional and must not block the core command.

**Backend order:**
- macOS: `say`
- Windows: PowerShell / SAPI
- Linux: `espeak-ng`, `espeak`, or `spd-say`
- optional `piper` when explicitly configured

**Rules:**
- `cw tasks` must work everywhere without speech tooling
- `--speak` warns once and continues if no backend exists
- use a bounded queue
- drop or coalesce if speech backlog builds

---

## Execution Plan

### Slice 1: Local task report primitive

**Files:**
- `internal/session/events.go`
- `internal/session/session.go`
- `internal/protocol/messages.go`
- `internal/node/handler.go`

**Deliverable:**
- local `task.report` event exists
- `ReportTask` request works against a local node

**Tests:**
- event constructor test
- `SessionManager.ReportTask` unit test

### Slice 2: MCP producer path

**Files:**
- `internal/mcp/server.go`

**Deliverable:**
- `codewire_report_task(session_id, summary, state)` works locally

**Tests:**
- tool validation tests
- happy path test against local node harness

### Slice 3: Node websocket outbound transport

**Files:**
- `internal/relay/agent.go`
- `internal/node/node.go`

**Deliverable:**
- node forwards task reports to relay over the existing websocket

**Tests:**
- outbound channel write path
- no-relay local behavior still works

### Slice 4: JetStream-backed relay task store

**Files:**
- `internal/relay/task_store.go`

**Deliverable:**
- relay can publish task events to `TASK_EVENTS`
- relay can update `TASK_LATEST`

**Tests:**
- publish path
- idempotent publish using `event_id`
- latest-state KV update

### Slice 5: Relay node ingest

**Files:**
- `internal/relay/node_handler.go`

**Deliverable:**
- authenticated node task reports become JetStream events

**Tests:**
- ingest test using authenticated node context

### Slice 6: Relay HTTP APIs

**Files:**
- `internal/relay/task_events.go`
- `internal/relay/relay.go`

**Deliverable:**
- snapshot endpoint
- SSE endpoint
- reconnect semantics using stream sequence

**Tests:**
- auth and membership checks
- replay from `Last-Event-ID`
- `stream.reset` when requested sequence has expired

### Slice 7: CLI relay client + command

**Files:**
- `internal/client/tasks.go`
- `cmd/cw/tasks.go`
- `cmd/cw/main.go`

**Deliverable:**
- `cw tasks`
- `cw tasks --watch`

**Tests:**
- client parser tests
- cursor persistence
- command wiring

### Slice 8: Optional speech

**Files:**
- `internal/client/tasks_tts.go`
- `cmd/cw/tasks.go`

**Deliverable:**
- `cw tasks --watch --speak`

**Tests:**
- backend selection
- graceful fallback when no backend is installed

### Slice 9: End-to-end integration

**Files:**
- `tests/integration_test.go`

**Deliverable:**
- MCP -> node -> relay -> JetStream -> SSE integration coverage

---

## Operational Notes

### JetStream config decisions to make explicitly

- stream retention window
- replication factor
- storage class
- dedupe window for `Nats-Msg-Id`
- KV bucket history depth

### Why this is better than the earlier in-memory relay hub

- replay survives relay instance restarts
- watch cursors are global, not process-local
- multiple relay instances can serve the same snapshot and watch traffic
- latest-state reads are no longer tied to one process's memory

### Why we still should not let nodes publish directly to NATS

- relay owns node auth and network scoping today
- direct node publish would spread credentials and policy outward
- relay-side enrichment keeps subjects and payloads canonical

---

## Open Follow-Ups

- If `TASK_LATEST` cardinality becomes very large, evaluate pagination and prefix-based scans carefully.
- If users need long-term historical browsing, add a paged history API backed by `TASK_EVENTS`.
- If we later want relay-independent consumers besides the CLI, expose the JetStream subjects through a separate internal service boundary rather than coupling clients directly to NATS.

---

## Concrete File-by-File Patch Plan

This section translates the architecture into the actual `codewire-cli` codebase.

### 1. Add NATS dependency

**File:**
- `go.mod`

**Change:**
- Add `github.com/nats-io/nats.go`

**Why:**
- There is currently no NATS or JetStream client in `codewire-cli`.

### 2. Extend relay runtime config

**Files:**
- `internal/relay/relay.go`
- `cmd/cw/main.go`

**Add to `relay.RelayConfig`:**

```go
type RelayConfig struct {
	// existing fields...
	NATSURL          string
	NATSCredsFile    string
	NATSSubjectRoot  string // default "tasks"
	TaskEventsStream string // default "TASK_EVENTS"
	TaskLatestBucket string // default "TASK_LATEST"
}
```

**CLI flags on `cw relay serve`:**
- `--nats-url`
- `--nats-creds`
- `--nats-subject-root` default `tasks`
- `--task-events-stream` default `TASK_EVENTS`
- `--task-latest-bucket` default `TASK_LATEST`

**Notes:**
- `node.name` already forbids dots because of NATS subject delimiters in [config.go](/home/noel/src/codewire/codewire-cli/internal/config/config.go), which is useful for this design.
- Keep stream and bucket names configurable, but ship sane defaults.

### 3. Introduce relay-side task store abstraction

**File:**
- Add `internal/relay/task_store.go`

**Purpose:**
- Keep HTTP handlers and websocket ingest decoupled from JetStream details.

**Define:**

```go
type TaskFilter struct {
	NetworkID string
	NodeName  string
	SessionID *uint32
	State     string
}

type TaskWatchEvent struct {
	Seq   uint64
	Event TaskEvent
}

type TaskWatcher interface {
	Events() <-chan TaskWatchEvent
	Close() error
}

type TaskStore interface {
	PublishNodeTaskReport(ctx context.Context, node store.NodeRecord, msg TaskReportMessage) (*LatestTaskValue, error)
	ListLatestTasks(ctx context.Context, filter TaskFilter) ([]LatestTaskValue, error)
	WatchTasks(ctx context.Context, filter TaskFilter, afterSeq uint64) (TaskWatcher, uint64, error)
	EarliestSeq(ctx context.Context, networkID string) (uint64, error)
}
```

**Notes:**
- `WatchTasks(...)` returns a watcher plus the earliest currently available stream sequence for reset checks.
- `PublishNodeTaskReport(...)` owns both stream publish and KV update.

### 4. Add JetStream-backed implementation

**File:**
- Add `internal/relay/nats_task_store.go`

**Responsibilities:**
- connect to JetStream context
- create/bind stream and KV bucket
- publish task events
- update latest-state KV
- list snapshot state
- open filtered watchers

**Suggested struct:**

```go
type NATSTaskStore struct {
	js               jetstream.JetStream
	kv               jetstream.KeyValue
	subjectRoot      string
	eventsStreamName string
	latestBucketName string
}
```

**Suggested helpers:**

```go
func subjectForTask(root, networkID, nodeName string, sessionID uint32) string
func kvKeyForTask(networkID, nodeName string, sessionID uint32) string
func subjectFilter(root string, filter TaskFilter) string
func ensureTaskInfra(ctx context.Context, js jetstream.JetStream, cfg RelayConfig) (jetstream.KeyValue, error)
```

**Stream config shape:**
- file storage
- limits retention
- configurable max age / max bytes later
- replicated in production
- subjects: `<root>.>`

**KV config shape:**
- bucket: `TASK_LATEST`
- keys compatible with subject token rules

### 5. Define task model types in relay package

**File:**
- Add `internal/relay/task_types.go`

**Move shared relay-side types here:**
- `TaskReportMessage`
- `TaskEvent`
- `LatestTaskValue`
- `TaskAPIEvent`

**Why:**
- these types are used by:
  - node websocket ingest
  - task store
  - HTTP handlers
  - tests

### 6. Local session event support

**Files:**
- `internal/session/events.go`
- `internal/session/session.go`

**Add in `events.go`:**
- `EventTaskReport`
- `TaskReportData`
- `NewTaskReportEvent(eventID, summary, state string) Event`

**Add in `session.go`:**

```go
type TaskReportForwardFunc func(sessionID uint32, sessionName, eventID, summary, state string, ts time.Time)

func (m *SessionManager) SetTaskReportForward(fn TaskReportForwardFunc)
func (m *SessionManager) ReportTask(sessionID uint32, summary, state string) error
```

**`ReportTask(...)` behavior:**
- resolve session
- normalize summary/state
- create `eventID` with `uuid.NewString()`
- append local event log
- publish local subscription event
- call forwarder if configured

### 7. Protocol additions

**File:**
- `internal/protocol/messages.go`

**Add fields to `Request`:**

```go
Summary string `json:"summary,omitempty"`
State   string `json:"state,omitempty"`
```

**Reuse response:**
- `Type: "TaskReported"`
- `ID: &sessionID`

### 8. Node request handling

**File:**
- `internal/node/handler.go`

**Add switch case:**

```go
case "ReportTask":
	sessionID, err := resolveMessageSession(manager, req.ID, req.Name)
	// ...
	if err := manager.ReportTask(sessionID, req.Summary, req.State); err != nil { ... }
	_ = writer.SendResponse(&protocol.Response{Type: "TaskReported", ID: &sessionID})
```

**Keep thin:**
- validation should remain in `SessionManager.ReportTask`

### 9. Node websocket outbound plumbing

**Files:**
- `internal/relay/agent.go`
- `internal/node/node.go`

**In `agent.go`:**
- extend `AgentConfig` with `Outbound <-chan []byte`
- if set, start a write goroutine that writes websocket text frames

**In `node.go`:**
- create `taskReportCh := make(chan []byte, 64)` when relay is configured
- pass `Outbound: taskReportCh` to `relay.RunAgent(...)`
- call `Manager.SetTaskReportForward(...)`
- forward callback marshals `TaskReportMessage` and does a non-blocking send

### 10. Relay websocket ingest

**File:**
- `internal/relay/node_handler.go`

**Signature change:**

```go
func RegisterNodeConnectHandler(mux *http.ServeMux, hub *NodeHub, st store.Store, tasks TaskStore)
```

**Read loop change:**
- parse inbound websocket text frames as JSON
- switch on `msg.Type`
- for `"TaskReport"` call:

```go
_, err := tasks.PublishNodeTaskReport(ctx, *node, msg)
```

**Important:**
- do not trust `network_id` or `node_name` from the payload
- enrich from the authenticated `node` loaded at websocket connect

### 11. Relay initialization

**File:**
- `internal/relay/relay.go`

**Patch shape:**
- in `RunRelay(...)`:
  - connect to NATS when task reporting is enabled
  - build JetStream context
  - instantiate `NATSTaskStore`
- pass task store to:
  - `RegisterNodeConnectHandler(...)`
  - `buildMux(...)`
  - `BuildRelayMux(...)` in tests

**Add helper:**

```go
func newTaskStore(ctx context.Context, cfg RelayConfig) (TaskStore, error)
```

### 12. Task HTTP handlers

**File:**
- Add `internal/relay/task_events.go`

**Functions:**

```go
func taskListHandler(st store.Store, tasks TaskStore) http.HandlerFunc
func taskEventsHandler(st store.Store, tasks TaskStore) http.HandlerFunc
```

**`taskListHandler`:**
- require user auth
- require membership
- parse filters
- call `tasks.ListLatestTasks(...)`
- return JSON

**`taskEventsHandler`:**
- require user auth
- require membership
- parse filters
- parse `Last-Event-ID` as `uint64`
- call `EarliestSeq(...)`
- emit `stream.reset` if requested sequence is too old
- otherwise call `WatchTasks(...)`
- write SSE using stream sequence as event id

### 13. MCP tool

**File:**
- `internal/mcp/server.go`

**Add tool:**

```go
{
	Name: "codewire_report_task",
	Description: "Report the current task status for a session",
	InputSchema: {
		"type": "object",
		"properties": {
			"session_id": {"type":"integer"},
			"summary": {"type":"string"},
			"state": {"type":"string","enum":["working","complete","blocked","failed"]}
		},
		"required": ["session_id","summary","state"]
	}
}
```

**Dispatch:**
- add `case "codewire_report_task": return toolReportTask(dataDir, args)`

**Implementation:**
- send `protocol.Request{Type:"ReportTask", ID:&sessionID, Summary:..., State:...}`

### 14. CLI relay client

**File:**
- Add `internal/client/tasks.go`

**Define:**

```go
type TaskSnapshot struct { ... } // mirror LatestTaskValue / API payload shape
type TaskEvent struct { ... }    // include Seq and Type

type WatchTasksOptions struct {
	NetworkID string
	NodeName  string
	SessionID *uint32
	State     string
}
```

**Functions:**

```go
func ListTasks(dataDir string, auth RelayAuthOptions, opts WatchTasksOptions) ([]TaskSnapshot, error)
func WatchTasks(ctx context.Context, dataDir string, auth RelayAuthOptions, opts WatchTasksOptions, out chan<- TaskEvent) error
```

**State file:**
- persist last seen stream sequence per network, same pattern as access-event cursor storage

### 15. CLI command

**Files:**
- Add `cmd/cw/tasks.go`
- Modify `cmd/cw/main.go`

**Command shape:**
- `cw tasks`
- `cw tasks --watch`
- `cw tasks --watch --speak`
- `cw tasks --json`
- `cw tasks --node`
- `cw tasks --session`
- `cw tasks --state`
- `cw tasks --network`

**Implementation split:**
- Cobra and rendering in `cmd/cw/tasks.go`
- HTTP/SSE transport in `internal/client/tasks.go`

### 16. Optional speech support

**File:**
- Add `internal/client/tasks_tts.go`

**Define:**

```go
type TaskSpeaker interface {
	Speak(ctx context.Context, text string) error
}
```

**Helpers:**
- `newTaskSpeaker(...)`
- backend probing for macOS / Windows / Linux

### 17. Tests

**Files to add or modify:**
- `internal/session/events_test.go`
- session tests near `internal/session/session.go`
- `internal/mcp/server.go` tests
- Add `internal/relay/nats_task_store_test.go`
- Modify `internal/relay/relay_network_test.go`
- Add `internal/client/tasks_test.go`
- Add `internal/client/tasks_tts_test.go`
- Modify `tests/integration_test.go`

**Priority order:**
1. `SessionManager.ReportTask`
2. NATS task store publish + KV update
3. relay websocket ingest
4. relay HTTP snapshot
5. relay SSE replay/reset
6. MCP tool
7. CLI client parser/reconnect
8. end-to-end integration

---

## Concrete Endpoint Shapes

### Snapshot response

`GET /api/v1/tasks`

```json
[
  {
    "event_id": "evt_123",
    "seq": 4821,
    "network_id": "project-alpha",
    "node_name": "dev-1",
    "session_id": 42,
    "session_name": "planner",
    "summary": "indexing relay tests",
    "state": "working",
    "timestamp": "2026-04-08T15:04:05Z"
  }
]
```

### SSE event

```text
id: 4821
event: task.report
data: {"seq":4821,"type":"task.report","event_id":"evt_123","network_id":"project-alpha","node_name":"dev-1","session_id":42,"session_name":"planner","summary":"indexing relay tests","state":"working","timestamp":"2026-04-08T15:04:05Z"}
```

### SSE reset

```text
id: 4800
event: stream.reset
data: {"seq":4800,"type":"stream.reset","network_id":"project-alpha"}
```

---

## Implementation Notes To Keep Straight

- Keep NATS private to the relay process boundary.
- Use stream sequence as the distributed SSE cursor.
- Use KV only for latest-state reads, not for watch delivery.
- Use `Nats-Msg-Id` with `event_id` to suppress duplicate publishes.
- Do not use `session_name` in subjects or KV keys.
- Do not make speech a dependency for the command to function.
