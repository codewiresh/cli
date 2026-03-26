# Relay Network-Wide Messaging

## Decision

Choose option 2:

- relay for network-scoped discovery, admission, and transport assistance
- direct encrypted peer transport for actual message traffic

The relay should not broker message bodies in steady state.

## Why This Architecture

This matches the broader network direction better than relay-brokered messaging:

- relay is control plane and transport assistance, not endpoint execution
- sessions still terminate at endpoints
- message bodies stay off the relay application path
- privacy, availability, and backpressure are peer concerns, not relay concerns

It also aligns with the existing `tailnet` package, which already provides:

- userspace WireGuard
- DERP fallback
- direct UDP upgrade when available
- `Listen` and `DialContextTCP` over the encrypted overlay

## Current State

This branch now has secure relay-scoped messaging for the supported command surface:

- `cw msg <node>:<session> ...`
- `cw inbox <node>:<session>`
- `cw request <node>:<session> ...`
- `cw listen --session <node>:<session>`
- `cw reply <request-id>` on the destination node for requests that were addressed to that local session

The security model is now:

- the network is the trust boundary
- the relay issues short-lived network-scoped runtime credentials
- nodes verify runtime credentials locally from a relay-published verifier bundle
- cross-node session-authored sends require a relay-issued sender delegation minted through the owning local node
- pending requests are bound to the addressed local replier session

Cross-node messaging now uses tailnet/WireGuard as the default transport under the same network auth model. Direct `/peer` WebSocket remains only as an explicit compatibility path for saved-server and direct-node cases.

## Current Branch State

This branch has a working secure remote path for:

- `cw msg <node>:<session> ...`
- `cw inbox <node>:<session>`
- `cw request <node>:<session> ...`
- `cw listen --session <node>:<session>`
- local `cw reply <request-id> --from <session>` for requests that originated remotely

Remote node resolution currently works in two ways:

- relay-backed discovery from `GET /api/v1/nodes` in the current network
- explicit fallback via `cw server add <node> <url>`

For relay-scoped messaging, the CLI now:

- asks the relay for a short-lived client runtime credential
- joins the network tailnet as an ephemeral client peer
- discovers the destination node through relay-backed coordination
- dials the destination node over tailnet TCP on the peer RPC port

The relay still uses WebSocket for the control plane:

- `/api/v1/tailnet/coordinate` for peer coordination
- DERP relay traffic when direct UDP is unavailable

But message payloads are no longer sent as application RPC over relay WebSockets.

The important current properties are:

- relay now issues signed runtime credentials for `client` and `node` subjects
- nodes now verify runtime credentials locally from a relay-published verifier bundle on `/peer`
- the CLI now requires a relay-issued client runtime credential when dialing `/peer`, unless an explicit runtime credential is passed with `--token`
- the relay now issues short-lived sender delegations for node-owned sessions
- remote `cw msg -f <local-session> <node>:<session> ...` and `cw request -f <local-session> <node>:<session> ...` now require a local-node-issued sender delegation
- pending requests are now bound to the addressed local replier session, so another local session cannot satisfy the same `request_id`
- message and request IDs are now opaque random IDs, not topology-derived values
- remote standalone `cw reply --from <node>:<session>` is now intentionally rejected because the CLI must not impersonate a session it does not own locally
- reply ownership is enforced for the supported request/reply flow by binding each request to the addressed local replier session

So the auth model and the default transport are now the intended ones for supported messaging behavior. What remains intentionally unsupported is remote session impersonation such as `cw reply --from other-node:session ...`.

## Current Secure State

The system is fully implemented and secure for these supported behaviors:

- client-authored remote `msg`, `request`, `inbox`, and targeted `listen`
- node-authored remote `msg` and `request` with `--from <local-session>` using sender delegation
- local `reply` to a local or remotely-originated request by the exact addressed replier session

The system intentionally does not support these behaviors:

- remote standalone `reply --from <other-node>:<session> ...`
- any cross-node send that tries to claim a session not owned by the current local node
- network-wide `listen --network`

Those are not partial security gaps. They are rejected by design because the CLI must not impersonate a remote node's session without local node mediation.

## Implemented Changes On This Branch

The key implementation changes are:

- added session locator parsing for `<session>` and `<node>:<session>` in the CLI
- added peer RPC support for remote `msg`, `inbox`, `request`, `reply`, and targeted `listen`
- added a node `/peer` endpoint for direct peer messaging
- added relay-backed node discovery with advertised `peer_url`
- added tailnet coordinator and DERP endpoints on the relay
- added persistent node tailnet peer listeners and ephemeral client tailnet peers
- made relay-scoped remote messaging prefer tailnet transport instead of direct `/peer`
- added coordinator keepalive pings so relay-scoped tailnet peers stay registered through idle periods
- added network-scoped runtime credentials for `client` and `node`
- added relay-published verifier bundles and local verification on `/peer`
- removed local node token and relay-session fallback admission from `/peer`
- added sender delegation issuance through the owning local node for session-authored remote `msg` and `request`
- bound pending requests to the addressed local replier session so only that session may reply
- replaced topology-derived message and request IDs with opaque random IDs
- rejected remote standalone session impersonation for `reply`

That means the remaining work is cleanup and transport convergence, not basic messaging correctness.

## Operational Examples

### Client-authored remote send

```bash
cw msg dev-2:coder "run the test suite"
```

Flow:

1. `cw` resolves `dev-2` through the current network relay or saved server config.
2. `cw` gets a short-lived client runtime credential for the network.
3. `cw` dials `dev-2` on `/peer`.
4. `dev-2` verifies network membership locally from the verifier bundle.
5. `dev-2` resolves `coder` locally and writes the inbox event.

No session sender is claimed.

### Session-authored remote send

```bash
cw msg -f planner dev-2:coder "start with auth"
```

Flow:

1. `cw` asks the current local node to resolve local session `planner`.
2. The local node asks the relay for a short-lived sender delegation for verb `msg`.
3. `cw` attaches that delegation and sends the peer RPC to `dev-2`.
4. `dev-2` verifies both the runtime credential and the sender delegation.
5. `dev-2` records the sender as authenticated session-authored traffic from the delegated session.

### Request with enforced reply ownership

```bash
cw request -f planner dev-2:coder "ready for review?"
```

Flow:

1. `planner` is authenticated through sender delegation as above.
2. `dev-2` resolves `coder` and records the request as pending for that exact local session.
3. `coder` may later run:

```bash
cw reply req_... "yes"
```

4. The destination node allows the reply only if the replying local session is the bound replier for that `request_id`.
5. A different local session on `dev-2` cannot satisfy that request.

### Rejected remote impersonation

```bash
cw reply req_... --from dev-2:coder "yes"
```

This is rejected from another machine or client process unless the reply is mediated by the owning local node. The CLI alone is not allowed to impersonate `dev-2:coder`.

## Core Principles

1. Keep local-node messaging unchanged.
2. Make relay-scoped messaging explicit with node-qualified addresses.
3. Use the relay for discovery and admission, not for message delivery in steady state.
4. Carry message traffic over an encrypted peer transport.
5. Keep inbox ownership on the destination node.
6. Avoid introducing a relay-side message bus.
7. Prefer request/response state owned by the initiating peer, not the relay.

## High-Level Model

There are three actor types:

- node: owns sessions and inbox state
- client: a `cw` process acting as a network-scoped peer
- relay: discovery, auth, coordinator, and DERP assistance

### Steady-State Flow

1. sender peer gets network-scoped credentials
2. sender asks relay/coordinator for peer connectivity info
3. sender opens an encrypted direct transport to the destination node
4. sender issues a message RPC to the destination node
5. destination node resolves the local session and writes local inbox state
6. destination replies directly to sender over the same peer transport

The relay may still assist transport via DERP, but it is not the messaging broker.

## Transport Choice

Use the tailnet/WireGuard path for peer transport.

That gives:

- encrypted node-to-node or client-to-node transport
- direct UDP where possible
- DERP relay fallback when direct connectivity is unavailable
- one transport family already used elsewhere in the repo

### Transitional Transport

Until the tailnet client-peer bootstrap exists in `codewire-cli`, the implementation may continue to use direct WebSocket peer transport:

- relay discovery returns an advertised node `peer_url`
- the CLI dials `/peer` on that node directly
- the destination node admits only network-scoped runtime identities and relay-issued sender delegations

This is acceptable only as a transport compatibility layer. The intended end state remains tailnet/WireGuard with the same auth model.

### Important Consequence

To make relay-scoped messaging work from a local `cw` client, the client must be able to act as a network-scoped tailnet peer, not just a relay-authenticated HTTP caller.

That means we need a client identity model in the relay/network system, not only node identities.

## Addressing

Introduce one user-facing session locator syntax:

- `<session>`: local session ID or name
- `<node>:<session>`: session on a remote node in the current network

Examples:

```bash
cw msg coder "run tests"
cw msg dev-2:coder "run tests"
cw msg -f dev-1:planner dev-2:coder "start with auth"

cw inbox dev-2:coder
cw request dev-2:coder "ready?"
cw reply req_01JS... "yes"
cw listen --session dev-2:coder
```

### Compatibility Rules

- unqualified locators remain node-local
- node-qualified locators use peer transport
- `--server` direct-node mode still bypasses relay discovery
- plain `cw listen` remains local-only unless the user explicitly targets a remote peer

## Identity and Auth

The network is the auth domain.

That means:

- every runtime identity is issued into exactly one network
- peer discovery is network-scoped
- peer admission is network-scoped
- sender delegation is network-scoped
- compromise of one network must not help an attacker impersonate identities in another network

### Runtime Identities

We need exactly two normal runtime identities:

1. `node` identity
2. `client` identity

Both are:

- issued or signed by the relay
- scoped to one network
- short-lived or renewable
- revocable
- sufficient for peer transport admission without loose hostname trust

Required claims:

```text
network_id
subject_kind   // node or client
subject_id     // stable node id/name or client id
issued_at
expires_at
jti
```

Optional claims may carry role or operator metadata, but transport admission must not depend on ad hoc strings in the peer payload.

### Sender Identity Is Separate From Transport Identity

This is the critical boundary.

Transport identity answers:

- is this peer a valid member of my network?

Sender identity answers:

- may this peer claim to speak as this session for this action?

Those are not the same question. A valid network client must not automatically gain the right to impersonate a session sender.

### Sender Modes

There are three sender modes:

1. user-authored
2. node-authored
3. client-authored

#### User-authored

- the sender is a human or tool using `cw`
- no session identity is claimed
- destination may record the authenticated client identity, but not a fake session sender

#### Node-authored

- the local node is speaking for one of its own sessions
- this requires a relay-issued sender delegation
- the delegation must be short-lived and action-scoped

#### Client-authored

- the client is speaking as itself, not as a session
- this is allowed for direct human/tool sends
- it does not imply authority to set `from_session`

### Sender Delegation

The relay must be the authority for session sender delegation.

If a caller wants to say:

```bash
cw msg -f planner dev-2:coder "start now"
```

then the local node, not the CLI alone, must obtain a relay-issued sender delegation proving:

- the source node is a valid member of the network
- the source node owns session `planner`
- the delegation allows the requested verb
- the delegation is fresh
- the delegation is optionally audience-bound to a destination node

Suggested claims:

```text
network_id
issuer
source_node
from_session_id
from_session_name
verbs           // msg, request, reply, listen if ever needed
audience_node   // optional
issued_at
expires_at
jti
```

The destination node verifies this delegation locally from relay-issued signing material. It should not need to call the relay on every message.

### Hard Rules

- never trust user-supplied `--from` by itself
- a client may not impersonate an arbitrary remote session
- a node may speak only for sessions it owns
- relay-issued sender delegations are the only accepted proof for session-authored cross-node sends
- delegated capabilities are narrow in verb, audience, and lifetime

### What This Means For Current Behavior

The current branch support for remote `--from <node>:<session>` should be treated as temporary routing behavior, not the final auth story.

The intended end state is:

- no raw node-token trust forwarded over peer RPC
- no display-only `--from` semantics for authenticated sends
- no cross-node session impersonation without relay-issued delegation

## Exact Data Model

### Session Locator

```go
type SessionLocator struct {
    Node string  `json:"node,omitempty"`
    ID   *uint32 `json:"id,omitempty"`
    Name string  `json:"name,omitempty"`
}
```

Rules:

- exactly one of `ID` or `Name` must be set
- `Node == ""` is local-only
- once the destination node resolves a name to an ID, all follow-up traffic should use the resolved ID

### Message Envelope

```go
type MessageEnvelope struct {
    MessageID string          `json:"message_id,omitempty"`
    RequestID string          `json:"request_id,omitempty"`
    Kind      string          `json:"kind"`
    From      *SessionLocator `json:"from,omitempty"`
    To        SessionLocator  `json:"to"`
    Body      string          `json:"body"`
    Delivery  string          `json:"delivery,omitempty"`
    CreatedAt time.Time       `json:"created_at"`
}
```

Use globally unique IDs:

- `msg_<ulid>`
- `req_<ulid>`

Do not derive IDs from node-local session IDs.

### Runtime Credential

Conceptually:

```go
type RuntimeCredential struct {
    NetworkID   string    `json:"network_id"`
    SubjectKind string    `json:"subject_kind"` // node or client
    SubjectID   string    `json:"subject_id"`
    IssuedAt    time.Time `json:"issued_at"`
    ExpiresAt   time.Time `json:"expires_at"`
    JTI         string    `json:"jti"`
    Signature   string    `json:"signature"`
}
```

This is used for peer transport admission.

### Sender Delegation

Conceptually:

```go
type SenderDelegation struct {
    NetworkID       string    `json:"network_id"`
    SourceNode      string    `json:"source_node"`
    FromSessionID   *uint32   `json:"from_session_id,omitempty"`
    FromSessionName string    `json:"from_session_name,omitempty"`
    Verbs           []string  `json:"verbs"`
    AudienceNode    string    `json:"audience_node,omitempty"`
    IssuedAt        time.Time `json:"issued_at"`
    ExpiresAt       time.Time `json:"expires_at"`
    JTI             string    `json:"jti"`
    Signature       string    `json:"signature"`
}
```

This is attached only when a peer is claiming a session sender identity.

## Peer RPC Surface

Do not invent a separate relay-only message protocol. Reuse the messaging verbs conceptually, but run them over peer RPC.

### Peer Request

```go
type PeerRequest struct {
    OpID      string           `json:"op_id"`
    Type      string           `json:"type"`
    SenderCap *SenderDelegation `json:"sender_cap,omitempty"`
    From      *SessionLocator  `json:"from,omitempty"`
    To        *SessionLocator  `json:"to,omitempty"`
    Session   *SessionLocator  `json:"session,omitempty"`
    RequestID string           `json:"request_id,omitempty"`
    Body      string           `json:"body,omitempty"`
    Tail      *uint            `json:"tail,omitempty"`
    Delivery  string           `json:"delivery,omitempty"`
    TimeoutS  *uint64          `json:"timeout_seconds,omitempty"`
}
```

### Peer Response

```go
type PeerResponse struct {
    OpID      string            `json:"op_id"`
    Type      string            `json:"type"`
    MessageID string            `json:"message_id,omitempty"`
    RequestID string            `json:"request_id,omitempty"`
    ReplyBody string            `json:"reply_body,omitempty"`
    From      *SessionLocator   `json:"from,omitempty"`
    Session   *SessionLocator   `json:"session,omitempty"`
    Messages  []MessageResponse `json:"messages,omitempty"`
    Event     *PeerMessageEvent `json:"event,omitempty"`
    Error     string            `json:"error,omitempty"`
}
```

Recommended request/response pairs:

- `MsgSend` -> `MsgSent`
- `MsgRead` -> `MsgReadResult`
- `MsgRequest` -> `MsgRequestResult`
- `MsgReply` -> `MsgReplySent`
- `MsgListen` -> `MsgListenAck`, then streamed `Event`

`op_id` is required because one encrypted peer connection may multiplex multiple operations.

## Node Responsibilities

The destination node remains the source of truth for:

- session resolution
- inbox writes
- local event publication
- PTY injection
- local authorization for which session may reply
- validation of sender delegation against relay-issued network trust roots

### Direct Message Delivery

For `MsgSend` to a remote node:

1. sender dials destination node over tailnet
2. destination validates peer transport identity for network membership
3. if `From` is present, destination validates `SenderCap`
4. destination resolves the local session
5. destination appends a `direct.message` event to recipient inbox
6. destination injects PTY text if requested
7. destination returns `MsgSent`

If the sender is another node speaking for a local session, that sender node may also append a local mirror copy after success.

### Remote Inbox Reads

For `cw inbox dev-2:coder`:

1. client dials `dev-2`
2. `dev-2` resolves `coder`
3. `dev-2` reads local message log
4. `dev-2` returns `MsgReadResult`

The relay is not involved after peer discovery.

## Cross-Node Request/Reply

This is the part that needs the most care.

### Ownership Model

- initiating peer owns the waiting request state
- destination node owns the local reply authorization ledger
- relay owns none of the message lifecycle beyond discovery and transport assistance

That is the key change from the relay-brokered design.

### Initiating Peer State

The initiating peer keeps the request open locally:

```go
type OutboundPendingRequest struct {
    RequestID   string
    OpID        string
    ToNode      string
    ToSessionID *uint32
    ToName      string
    Deadline    time.Time
    Status      string // created, delivered, completed, timed_out, failed
}
```

If the initiator is:

- a `cw` client, the client process holds this state
- a node speaking for a session, that node holds this state

### Destination Node Ledger

The destination node needs a durable local ledger so `cw reply req_...` can work later:

```go
type InboundRelayRequest struct {
    RequestID               string    `json:"request_id"`
    FromNode                string    `json:"from_node"`
    FromSessionID           *uint32   `json:"from_session_id,omitempty"`
    FromSessionName         string    `json:"from_session_name,omitempty"`
    ToNode                  string    `json:"to_node"`
    ToSessionID             uint32    `json:"to_session_id"`
    ToSessionName           string    `json:"to_session_name,omitempty"`
    ReturnPeer              string    `json:"return_peer"`
    ReturnOpID              string    `json:"return_op_id"`
    AllowedReplierSessionID uint32    `json:"allowed_replier_session_id"`
    CreatedAt               time.Time `json:"created_at"`
    ExpiresAt               time.Time `json:"expires_at"`
    Status                  string    `json:"status"` // pending, replying, completed, expired
}
```

This ledger is required because the destination node must remember:

- that the request is real
- which local session may answer it
- where to send the reply back

Persist it to disk near session metadata, not just in memory.

### Request Flow

1. initiating peer dials destination node
2. initiator sends `MsgRequest`
3. destination node resolves recipient session
4. destination writes `InboundRelayRequest`
5. destination appends `message.request` event to recipient inbox
6. destination returns an ACK-level response indicating delivery succeeded
7. initiator keeps waiting on the same peer RPC operation
8. recipient session later runs `cw reply req_... "..."` locally
9. destination node checks `InboundRelayRequest`
10. destination node verifies the replying session is allowed
11. destination node sends `MsgRequestResult` back to the initiating peer using `return_peer + return_op_id`
12. both sides mark request completed

### Why This Is Better Than Relay-Owned Pending State

- fewer relay semantics to persist
- no relay waiter registry
- no relay-side message broker
- reply path follows the same encrypted peer transport as the request path

### Important Limitation

If the initiating peer disappears before the reply arrives, the request cannot complete. That is acceptable in phase 1.

## Request/Reply State Machine

### Initiating Peer States

```text
created
delivered
completed
timed_out
failed
```

### Destination Node States

```text
pending
replying
completed
expired
```

Transitions:

1. initiator sends `MsgRequest`, state `created`
2. destination accepts and writes ledger, initiator state `delivered`, destination state `pending`
3. local session replies, destination state `replying`
4. reply reaches initiator, both move to `completed`
5. timeout or disconnect moves initiator to `timed_out` or `failed`
6. destination cleanup job ages out stale ledger rows to `expired`

## Mirroring

Mirror event means writing a copy of the sent message or request into the sender's own local history.

Example:

- `dev-1:planner` sends a request to `dev-2:coder`
- `dev-2` writes the real inbox event for `coder`
- `dev-1` may also write a mirror copy into `planner`'s history

Mirror behavior should be best-effort only:

- recipient delivery determines success
- mirror failure must not fail `cw msg` or `cw request`

## Listen Semantics

Two useful modes remain:

1. local listen:
   - `cw listen`
   - node-local only
2. targeted remote listen:
   - `cw listen --session dev-2:coder`
   - peer dials `dev-2` directly and subscribes there

Defer true network-wide `cw listen --network` until later. It is much less natural in a direct-peer design and would require either:

- multi-peer fanout from the client
- a relay-exported event tap

That should not block message delivery.

## Delivery Guarantees

Phase 1 should be explicit:

- peer transport is encrypted
- direct UDP is preferred when available
- DERP fallback is acceptable and still encrypted
- destination node must be online
- initiating peer must stay alive until request completes
- no offline message queue
- no relay-side delivery journal

Recommended timeout windows:

- peer dial timeout: short, for example 5s to 10s
- request timeout exposed to users: default 60s
- destination ledger retention: request timeout plus cleanup slack

## Relay Responsibilities

The relay is still important, but narrower:

- authenticate clients and nodes
- enforce network scope
- provide peer discovery and admission
- issue runtime credentials
- issue sender delegations for node-authored cross-node sends
- provide DERP assistance when direct transport fails
- expose revocation and policy decisions

The relay should not:

- own inbox contents
- broker message bodies in steady state
- maintain request waiter state for normal request/reply

## Security Invariants

The implementation is only correct if these hold:

1. network is the primary trust boundary
2. peer admission is impossible outside the network
3. sender session impersonation is impossible without relay-issued delegation
4. a client runtime identity is never equivalent to a session sender identity
5. node-authored delegation is bounded by node ownership of the source session
6. runtime credentials and sender delegations are revocable and expire quickly
7. transport choice may change, but the auth model must not

## Trust Roots and Verification

Every network needs its own signing authority and verifier bundle.

Minimum model:

- one network issuer per network
- one active signing keyset per issuer
- key IDs so credentials can be rotated without breaking live peers
- verifier bundles cached by nodes and clients

The relay is the online issuer. Nodes and clients are offline verifiers between refreshes.

### Verifier Bundle

Conceptually:

```go
type VerifierBundle struct {
    NetworkID string         `json:"network_id"`
    Issuer    string         `json:"issuer"`
    Keys      []VerifierKey  `json:"keys"`
    Version   uint64         `json:"version"`
    ExpiresAt time.Time      `json:"expires_at"`
}
```

Rules:

- a verifier bundle is valid only for one network
- nodes and clients refresh bundles periodically from the relay
- credential verification must fail closed when the bundle is expired
- old keys may remain in the bundle during rotation overlap

Do not couple verification to ad hoc HTTP callbacks on the hot path. Verification should be local for normal message traffic.

## Issuance Flows

### Node Runtime Credential

1. node authenticates to the relay with its node bootstrap secret
2. relay issues a short-lived network runtime credential for subject kind `node`
3. node stores it in memory only
4. node refreshes it before expiry

### Client Runtime Credential

1. `cw` authenticates to the relay as a user/operator in one network
2. relay issues a short-lived network runtime credential for subject kind `client`
3. `cw` keeps it in memory for the current process
4. `cw` refreshes or reissues it when needed

### Sender Delegation

The CLI must not ask the relay for session sender authority directly. The local node must be in the loop.

1. `cw msg -f planner dev-2:coder ...` reaches the local node over the existing local control path
2. local node resolves `planner` to a local session it owns
3. local node requests a sender delegation from the relay
4. relay issues a short-lived delegation scoped to that node, session, verb, and optional audience
5. local node returns the delegation to the local CLI for attachment to the peer RPC

This keeps session authority anchored in the owning node instead of the human client.

## Admission and Authorization

### Peer Transport Admission

When a peer connection is established:

1. caller presents a runtime credential
2. callee validates issuer, key ID, signature, network, expiry, and subject kind
3. callee maps the credential to a peer principal
4. only then may peer RPC begin

Transport admission only proves network membership. It does not authorize session impersonation.

### Session-Sender Authorization

When a peer RPC includes `From`:

1. destination verifies a sender delegation is present
2. destination validates issuer, signature, network, expiry, and verb
3. destination checks the delegation audience if set
4. destination checks that `From` exactly matches the delegated session identity
5. destination records the sender as authenticated session-authored traffic

Without a valid sender delegation:

- `From` must be ignored for authorization
- the request is either rejected or downgraded to client-authored, depending on the verb and UX decision

Recommended default:

- reject `--from` when delegation is missing or invalid
- allow the same request without `--from` as client-authored traffic

## Replay, Revocation, and Rotation

### Replay Protection

Both runtime credentials and sender delegations need replay resistance.

Required controls:

- short expiries
- unique `jti`
- destination-side replay cache for sender delegation `jti`
- request `op_id` uniqueness per live peer connection

The replay cache only needs to hold entries for the maximum delegation lifetime plus clock skew.

### Revocation

We need two levels:

1. ordinary expiry-based invalidation
2. emergency revocation before expiry

Ordinary operation should prefer short lifetimes over a constantly queried revocation endpoint.

Emergency revocation should support:

- node removed from network
- client removed from network
- key compromise
- session delegation mis-issuance

Recommended mechanism:

- relay publishes a compact denylist keyed by `jti` and, when needed, by subject
- nodes and clients refresh it on the same cadence as verifier bundles
- destination rejects credentials or delegations present in the denylist

### Key Rotation

Rotation rules:

- new signing keys are published before issuance starts
- verifiers accept both old and new keys during overlap
- relay stops issuing old keys
- old keys are removed only after all outstanding credential lifetimes expire

The wire protocol must carry key IDs. Do not infer verification keys by trial.

## Command Authorization Matrix

The allowed sender modes should be explicit.

### `cw msg`

- no `--from`: allowed as client-authored
- local `--from <session>`: existing local-node semantics
- remote `--from <session>` where the session is owned by the current local node: allowed only with valid sender delegation minted through that local node
- remote `--from <node>:<session>` naming some other node: rejected

### `cw request`

- no `--from`: allowed as client-authored
- local `--from <session>`: existing local-node semantics
- remote `--from <session>` where the session is owned by the current local node: allowed only with valid sender delegation minted through that local node
- remote `--from <node>:<session>` naming some other node: rejected

### `cw reply`

- local standalone reply: existing local-node semantics
- reply to a remotely-originated request from the destination node's addressed local session: allowed
- remote standalone reply with `--from <node>:<session>` from a different machine or client: rejected by design

### `cw inbox`

- remote inbox reads require only peer transport admission unless a tighter policy is chosen later
- if inbox privacy should be stricter, add a separate read policy later rather than overloading sender delegation

### `cw listen`

- targeted remote listen requires peer transport admission
- defer any network-wide observe mode until there is an explicit read policy for it

## Migration From Current Branch State

The current branch already implements the secure auth model for supported messaging behavior. Migration work now means removing compatibility layers, not redesigning the trust boundary.

### Transitional Rules

1. do not add any new flow that lets the CLI impersonate a session it does not own locally
2. keep client-authored remote `msg`, `inbox`, `request`, and targeted `listen` working
3. keep session-authored remote `msg` and `request` gated on the sender delegation path
4. keep local-session reply ownership enforcement in place for both local and remotely-originated requests
5. keep `/peer` admission runtime-credential-only

### Compatibility Window

The old mixed-admission period is over for `/peer`.

- `/peer` is runtime-credential-only
- authenticated session senders still require sender delegation through the owning local node
- remote standalone session impersonation remains unsupported

Compatibility work that remains is transport migration, not auth fallback.

## Rollout Plan

### Phase 0: Freeze Auth Surface

- stop widening the provisional `/peer` auth behavior
- document current behavior as compatibility-only
- reject any new feature that depends on raw `--from` trust

### Phase 1: Network Trust Package

- add a shared verifier package for runtime credentials and sender delegations
- define network-scoped verifier bundle refresh and cache rules
- define `kid`, `jti`, expiry, and clock-skew handling

### Phase 2: Runtime Credentials

- issue short-lived runtime credentials for `node` and `client`
- require them on peer transport admission
- remove ad hoc token-mixing from `/peer`

### Phase 3: Sender Delegation

- add relay issuance for sender delegations
- add local node RPC for CLI delegation requests
- require valid sender delegation for any remote session-authored `--from`

### Phase 4: Transport/Auth Convergence

- make direct WebSocket `/peer` and tailnet transports share the same principal and verifier model
- keep peer RPC semantics stable while swapping transport underneath

### Phase 5: Tailnet As Default Transport

- let `cw` join the network as a first-class client peer
- prefer tailnet transport for remote messaging
- keep direct `/peer` only as an explicit compatibility path, if at all

### Phase 6: Command Surface Hardening

- harden policy for remote inbox reads and targeted listen
- decide whether client-authored remote sends need extra policy constraints
- remove provisional auth branches entirely

## Definition Of Complete

Messaging is complete when all supported commands obey the network auth model and no insecure impersonation path remains. That means:

- network-scoped runtime credentials are required for peer admission
- session-authored remote sends are possible only through sender delegation minted by the owning local node
- request ownership is bound to the addressed local replier session
- unsupported remote impersonation flows are rejected explicitly
- transport may be WebSocket `/peer` or tailnet, but the auth model is identical

By that definition, the current branch is complete for the supported messaging surface. The remaining work is transport convergence and policy hardening, not core messaging security.

## Implementation Breakdown

This is the auth-first implementation map for the current repo.

### 1. Shared Auth Package

Add a shared package for verification, principal extraction, and replay checks.

Recommended files:

- `codewire-cli/internal/networkauth/types.go`
- `codewire-cli/internal/networkauth/verify.go`
- `codewire-cli/internal/networkauth/cache.go`
- `codewire-cli/internal/networkauth/replay.go`

Responsibilities:

- parse runtime credentials and sender delegations
- verify signatures against a network verifier bundle
- validate `network_id`, `kid`, `exp`, `jti`, and subject fields
- expose a stable verified-principal type to relay, node, and client code

Do not spread credential verification logic across `relay`, `node`, and `peer` packages.

### 2. Relay Issuance and Bundle Endpoints

The relay becomes the narrow issuer and bundle publisher.

Primary code:

- `codewire-cli/internal/relay/relay.go`
- `codewire-cli/internal/relay/node_handler.go`

Add:

- runtime credential issuance for node and client subjects
- sender delegation issuance for node-owned sessions
- verifier bundle publication
- denylist publication for emergency revocation

Recommended new files:

- `codewire-cli/internal/relay/issuer.go`
- `codewire-cli/internal/relay/verifier_bundle.go`
- `codewire-cli/internal/relay/revocation.go`

### 3. Node Runtime Integration

Primary code:

- `codewire-cli/internal/node/node.go`
- `codewire-cli/internal/session/session.go`

Node responsibilities:

- refresh verifier bundle and denylist
- hold a short-lived node runtime credential in memory
- require runtime credentials on inbound peer transport
- verify sender delegations on inbound peer RPC
- expose a local control-plane call to mint sender delegations for owned sessions

Recommended new files:

- `codewire-cli/internal/node/auth.go`
- `codewire-cli/internal/node/delegation.go`

### 4. CLI Runtime Integration

Primary code:

- `codewire-cli/cmd/cw/main.go`
- `codewire-cli/internal/peerclient/client.go`
- `codewire-cli/internal/client/client.go`

CLI responsibilities:

- obtain a client runtime credential for the current network
- attach it on remote peer transport
- ask the local node for sender delegations when `--from <local-session>` is used
- refuse remote `--from` when no local owning node is available

Keep direct `--server` mode separate. It is not the network-wide trust path.

### 5. Peer RPC Changes

Primary code:

- `codewire-cli/internal/peer/protocol.go`
- `codewire-cli/internal/peer/server.go`
- `codewire-cli/internal/peerclient/commands.go`

Needed changes:

- treat `SenderCap` as a real verified field, not a placeholder
- ensure `From` must match the verified delegation exactly
- record authenticated sender metadata in message/request events
- reject invalid delegated traffic consistently across `msg`, `request`, and `reply`

### 6. Tailnet Transport Convergence

Primary reuse points:

- `codewire-cli/cmd/cw/ssh_cmd.go`
- `codewire-cli/internal/peer/bootstrap.go`
- `tailnet/conn.go`

Goal:

- one peer identity model regardless of direct `/peer` or tailnet transport
- tailnet becomes the preferred transport, not a different auth path

### 7. Request/Reply Ledger Hardening

Primary code:

- `codewire-cli/internal/session/session.go`
- remote request ledger files

Needed changes:

- store authenticated sender metadata, not only routing metadata
- make reply authorization depend on the local session plus validated original request
- define cleanup semantics for expired delegations versus still-pending requests

### 8. Test Plan By Security Boundary

#### Unit tests

- verifier bundle parsing and cache expiry
- runtime credential verification
- sender delegation verification
- replay cache behavior
- exact-match enforcement between `From` and delegation claims

#### Integration tests

- node runtime credential admission on `/peer`
- client runtime credential admission on `/peer`
- remote `msg` without `--from` succeeds as client-authored
- remote `msg --from` fails without delegation
- remote `msg --from` succeeds with a valid delegation from the owning node
- remote `reply --from` succeeds only for the owning node/session
- revoked credential or delegation is rejected before expiry

#### Transport tests

- the same runtime credential verifies on direct `/peer` and tailnet
- transport swap does not change sender-auth behavior

## Suggested Implementation Order

1. add `internal/networkauth`
2. add relay verifier bundle and issuer endpoints
3. require runtime credentials on `/peer`
4. add local node delegation issuance
5. require sender delegations for remote `--from`
6. harden request/reply ledger around verified sender identity
7. move the same principal model onto tailnet transport
8. delete provisional auth branches

## Recommendation

Treat the current remote messaging work as a transport and UX prototype, not as the final security model.

The correct long-term system is:

- one auth domain per network
- one verifier model for every transport
- relay-issued runtime credentials for nodes and clients
- relay-issued sender delegations for session-authored cross-node traffic
- tailnet as the preferred encrypted transport, not a separate trust system

That preserves the direct-peer architecture while making the security boundary coherent.
