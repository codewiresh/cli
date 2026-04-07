# Peer-to-Peer Node Messaging via Tailnet

**Status:** Shipped in v0.3.8

**Goal:** Enable `cw msg node:session` between two cw nodes on the same relay network using tailnet peer-to-peer connections via DERP relay.

**Architecture:** Each node registers its WireGuard public key with the relay's tailnet coordinator over WebSocket. When node A wants to message node B, it subscribes to B's peer info, establishes a WireGuard tunnel through the DERP relay, and sends the message over a TCP connection inside the tunnel.

---

## Bugs Fixed

### 1. Node subscribe restriction (v0.3.7)

**File:** `internal/relay/tailnet.go:132-138`

The coordinator handler only allowed client credentials to subscribe to node peer info. Node credentials were rejected with "only client peers may subscribe". Removed the 6-line `if` block. Network scoping via `claims.NetworkID` in `StablePrincipalUUID` prevents cross-network subscriptions.

### 2. Coordinator UUID collision (v0.3.8)

**Files:** `internal/peer/tailnet_transport.go`, `internal/relay/tailnet.go`

When a node runs `cw msg`, `DialNetworkPeerTCP` opens a second coordinator WebSocket. Both the persistent listener and the dial connection computed the same stable UUID via `StablePrincipalUUID(networkID, "node", nodeName)`. The second `Register` call replaced the listener's channel, then the listener's deferred `Deregister` removed the new registration. Peer info exchange never completed.

**Fix:** Dial connections pass `?role=dial` query param to the coordinate endpoint. The relay handler assigns `uuid.New()` when `role=dial`. On the client side, `tailnetIDForDial` always returns `uuid.New()`.

### 3. Relay auth token for OIDC mode (v0.3.8)

**Files:** `charts/codewire-relay/templates/deployment.yaml`, `charts/codewire-relay/templates/secret.yaml`

The Helm chart didn't pass `--auth-token` in OIDC mode. `cw env create --network` needs to create invite tokens via `/api/v1/invites`, which requires admin auth. Added optional `relay.authToken` support that works alongside OIDC.

---

## Usage

```bash
# Both environments must be on the same relay network with cw node running.
# Create sessions on both sides:
cw env exec <alpha-id> -- cw run alpha -- bash -c "sleep 3600"
cw env exec <bravo-id> -- cw run bravo -- bash -c "sleep 3600"

# Send message from alpha to bravo (--from required for sender delegation):
cw env exec <alpha-id> -- cw msg --from alpha "<bravo-node>:bravo" "hello"

# Check inbox on bravo:
cw env exec <bravo-id> -- cw inbox bravo
```

## Verification

1. No "only client peers may subscribe" error
2. No "timeout waiting for tailnet peer info" (UUID collision fixed)
3. Both nodes exchange peer info via coordinator (peer_update messages)
4. DERP relay connection established between nodes
5. `cw msg --from <session> node:session` delivers message
6. Bidirectional messaging confirmed
