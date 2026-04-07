# Relay Security Hardening Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Close verified security vulnerabilities in the relay auth, token storage, and coordination layers.

**Architecture:** Seven self-contained fixes applied to the existing relay codebase. Each fix is independently testable and deployable. No new dependencies. All changes are backward-compatible except Task 2 (node token hashing), which requires a one-time migration.

**Tech Stack:** Go, modernc.org/sqlite, Ed25519 (crypto/ed25519), standard library `net/url`

---

## Phase 1: Small fixes, high impact

### Task 1: Require non-empty audienceNode in SignSenderDelegation

**Files:**
- Modify: `internal/networkauth/runtime.go:184-236`
- Modify: `internal/networkauth/runtime_test.go`

**Context:** `SignSenderDelegation` accepts an empty `audienceNode` parameter. When empty, the receiving node's check at `internal/node/node.go:518` (`claims.AudienceNode != "" && ...`) is skipped, meaning the delegation is accepted by ANY node in the network. This turns a node-scoped sender capability into a network-wide one.

**Step 1: Write the failing test**

Add to `internal/networkauth/runtime_test.go`:

```go
func TestSignSenderDelegation_RejectsEmptyAudienceNode(t *testing.T) {
	state, err := NewIssuerState("project-alpha")
	if err != nil {
		t.Fatalf("NewIssuerState: %v", err)
	}

	sessionID := uint32(1)
	now := time.Now().UTC()

	_, _, err = SignSenderDelegation(state, "dev-1", &sessionID, "planner", nil, []string{"msg"}, "", now, time.Minute)
	if err == nil {
		t.Fatal("expected error for empty audienceNode, got nil")
	}

	_, _, err = SignSenderDelegation(state, "dev-1", &sessionID, "planner", nil, []string{"msg"}, "   ", now, time.Minute)
	if err == nil {
		t.Fatal("expected error for whitespace-only audienceNode, got nil")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `TMPDIR=/home/noel/tmp go test ./internal/networkauth/ -run TestSignSenderDelegation_RejectsEmptyAudienceNode -v -count=1`
Expected: FAIL -- both cases return nil error since audienceNode is not validated.

**Step 3: Write minimal implementation**

In `internal/networkauth/runtime.go`, add validation after the existing `verbs` check (after line 196):

```go
if strings.TrimSpace(audienceNode) == "" {
	return "", nil, fmt.Errorf("audience node is required")
}
```

**Step 4: Run test to verify it passes**

Run: `TMPDIR=/home/noel/tmp go test ./internal/networkauth/ -run TestSignSenderDelegation_RejectsEmptyAudienceNode -v -count=1`
Expected: PASS

**Step 5: Run full networkauth test suite**

Run: `TMPDIR=/home/noel/tmp go test ./internal/networkauth/ -v -count=1`
Expected: PASS -- existing `TestSignAndVerifySenderDelegation` already passes `"dev-2"` as audienceNode.

**Step 6: Commit**

```bash
git add internal/networkauth/runtime.go internal/networkauth/runtime_test.go
git commit -m "security: require non-empty audienceNode in sender delegations

Empty audienceNode bypassed the audience check on receiving nodes,
turning a node-scoped delegation into a network-wide capability."
```

---

### Task 2: Hash node tokens before storage

**Files:**
- Modify: `internal/store/store.go:82-93` (NodeRecord struct)
- Modify: `internal/store/store.go:254` (Store interface -- rename method)
- Modify: `internal/store/sqlite.go:90-100` (schema), `sqlite.go:229` (index), `sqlite.go:852-877` (NodeRegister), `sqlite.go:939-955` (NodeGetByToken)
- Modify: `internal/relay/relay.go:1136-1139` (move hash helper)
- Modify: `internal/relay/node_handler.go:22-32` (hash before lookup)
- Modify: `internal/relay/ssh.go:46` (hash before lookup)
- Modify: `internal/relay/back_handler.go:74` (hash before lookup)
- Modify: `internal/store/sqlite_test.go`

**Context:** Node tokens are stored as plaintext in the `nodes.token` column. Enrollment tokens already use SHA-256 hashing via `hashEnrollmentToken()`. A DB compromise exposes all node tokens directly. The fix: store `SHA-256(token)` in a `token_hash` column, look up by hash.

**Important:** The plaintext token is returned to the node during enrollment (enrollment.go:103) and never stored on the relay side again. The node sends it as `Authorization: Bearer <token>` on every connection. We hash on receive and look up by hash. The `NodeRecord.Token` field in Go becomes the hash, not the plaintext.

**Step 1: Write the failing store test**

Add to `internal/store/sqlite_test.go`:

```go
func TestNodeGetByTokenHash(t *testing.T) {
	s, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	tokenHash := hashNodeToken("secret-node-token")
	now := time.Now().UTC()

	err = s.NodeRegister(ctx, NodeRecord{
		NetworkID:    "net-1",
		Name:         "dev-1",
		TokenHash:    tokenHash,
		AuthorizedAt: now,
		LastSeenAt:   now,
	})
	if err != nil {
		t.Fatalf("NodeRegister: %v", err)
	}

	got, err := s.NodeGetByTokenHash(ctx, tokenHash)
	if err != nil {
		t.Fatalf("NodeGetByTokenHash: %v", err)
	}
	if got == nil || got.Name != "dev-1" {
		t.Fatalf("expected dev-1, got %v", got)
	}

	miss, err := s.NodeGetByTokenHash(ctx, hashNodeToken("wrong-token"))
	if err != nil {
		t.Fatalf("NodeGetByTokenHash miss: %v", err)
	}
	if miss != nil {
		t.Fatal("expected nil for wrong token hash")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `TMPDIR=/home/noel/tmp go test ./internal/store/ -run TestNodeGetByTokenHash -v -count=1`
Expected: compile error -- `TokenHash` field, `NodeGetByTokenHash`, and `hashNodeToken` don't exist yet.

**Step 3: Add hashNodeToken to store package**

Create a shared hash function in `internal/store/store.go` (near the NodeRecord struct):

```go
// HashNodeToken returns the SHA-256 hex digest of a raw node token.
func HashNodeToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}
```

Add imports: `"crypto/sha256"`, `"encoding/hex"`, `"strings"`.

**Step 4: Rename NodeRecord.Token to TokenHash**

In `internal/store/store.go`, change the `NodeRecord` struct:

```go
type NodeRecord struct {
	NetworkID    string    `json:"network_id"`
	Name         string    `json:"name"`
	TokenHash    string    `json:"token_hash"`
	PeerURL      string    `json:"peer_url,omitempty"`
	GitHubID     *int64    `json:"github_id,omitempty"`
	OwnerSubject string    `json:"owner_subject,omitempty"`
	AuthorizedBy string    `json:"authorized_by,omitempty"`
	EnrollmentID string    `json:"enrollment_id,omitempty"`
	AuthorizedAt time.Time `json:"authorized_at"`
	LastSeenAt   time.Time `json:"last_seen_at"`
}
```

**Step 5: Update Store interface**

In `internal/store/store.go`, rename the method:

```go
NodeGetByTokenHash(ctx context.Context, tokenHash string) (*NodeRecord, error)
```

**Step 6: Update SQLite schema and implementation**

In `internal/store/sqlite.go`:

1. Add migration after the existing `addColumnIfNotExists` calls to rename `token` to `token_hash`:

```go
s.addColumnIfNotExists("nodes", "token_hash", "TEXT NOT NULL DEFAULT ''")
s.migrateNodeTokenToHash()
```

Add the migration method:

```go
func (s *SQLiteStore) migrateNodeTokenToHash() {
	// One-time migration: hash any plaintext tokens still in the token column
	// and copy to token_hash, then clear the plaintext.
	rows, err := s.db.Query("SELECT network_id, name, token FROM nodes WHERE token != '' AND (token_hash = '' OR token_hash IS NULL)")
	if err != nil {
		return
	}
	defer rows.Close()

	type pending struct{ networkID, name, token string }
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.networkID, &p.name, &p.token); err != nil {
			continue
		}
		todo = append(todo, p)
	}
	for _, p := range todo {
		hash := HashNodeToken(p.token)
		s.db.Exec("UPDATE nodes SET token_hash = ?, token = '' WHERE network_id = ? AND name = ?", hash, p.networkID, p.name)
	}
}
```

2. Update `NodeRegister` to use `token_hash` column instead of `token`.

3. Rename `NodeGetByToken` to `NodeGetByTokenHash` -- query by `token_hash` column.

4. Update the unique index from `token` to `token_hash`.

5. Update all `Scan` calls to scan into `TokenHash` instead of `Token`.

**Step 7: Update all callers to hash before lookup**

In each file that calls `NodeGetByToken`, hash the raw token first:

- `internal/relay/node_handler.go:27` -- `st.NodeGetByTokenHash(r.Context(), store.HashNodeToken(token))`
- `internal/relay/node_handler.go:93` -- same pattern
- `internal/relay/ssh.go:46` -- `st.NodeGetByTokenHash(ctx, store.HashNodeToken(string(pass)))`
- `internal/relay/back_handler.go:74` -- same pattern

In `internal/relay/enrollment.go:85-89`, hash the token before storing:

```go
nodeToken := generateToken()
node := store.NodeRecord{
	NetworkID:    consumed.NetworkID,
	Name:         nodeName,
	TokenHash:    store.HashNodeToken(nodeToken),
	...
}
```

The plaintext `nodeToken` is still returned to the caller (enrollment.go:103).

**Step 8: Run all store tests**

Run: `TMPDIR=/home/noel/tmp go test ./internal/store/ -v -count=1`
Expected: PASS (update existing `TestNodeGetByToken` in sqlite_test.go to use the new names).

**Step 9: Fix compilation in relay and run relay tests**

Run: `TMPDIR=/home/noel/tmp go test ./internal/relay/ -v -count=1 -timeout 120s`
Expected: PASS after updating all callers.

**Step 10: Commit**

```bash
git add internal/store/ internal/relay/ 
git commit -m "security: hash node tokens with SHA-256 before storage

Node tokens were stored plaintext in the nodes table. Enrollment tokens
already used SHA-256 hashing. Now node tokens follow the same pattern.
Includes one-time migration for existing plaintext tokens."
```

---

### Task 3: Require session specifier in observer grant matching

**Files:**
- Modify: `internal/node/node.go:623-653`
- Create: `internal/node/observer_test.go`

**Context:** `matchObserverSession` returns nil (success) when claims have nil SessionID and empty SessionName. While `SignObserverDelegation` already validates that at least one is set during signing, the receiving node should independently enforce this as defense-in-depth (especially if the Ed25519 key is compromised).

**Step 1: Write the failing test**

Create `internal/node/observer_test.go`:

```go
package node

import (
	"testing"

	"github.com/codewiresh/codewire/internal/networkauth"
	"github.com/codewiresh/codewire/internal/peer"
)

func TestMatchObserverSession_RejectsUnboundedGrant(t *testing.T) {
	n := &Node{} // matchObserverSession only uses n.Manager for name resolution, which we won't hit

	locatorID := uint32(5)
	locator := &peer.SessionLocator{ID: &locatorID}

	// Claims with no session binding -- should be rejected
	claims := &networkauth.ObserverDelegationClaims{
		SessionID:   nil,
		SessionName: "",
	}

	err := n.matchObserverSession(locator, claims)
	if err == nil {
		t.Fatal("expected error for observer grant with no session specifier, got nil")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `TMPDIR=/home/noel/tmp go test ./internal/node/ -run TestMatchObserverSession_RejectsUnboundedGrant -v -count=1`
Expected: FAIL -- `matchObserverSession` returns nil for unbounded claims when locator has an ID.

**Step 3: Write minimal implementation**

In `internal/node/node.go`, add a check at the top of `matchObserverSession`, after the nil locator check (after line 626):

```go
if claims.SessionID == nil && strings.TrimSpace(claims.SessionName) == "" {
	return fmt.Errorf("observer grant must specify session_id or session_name")
}
```

**Step 4: Run test to verify it passes**

Run: `TMPDIR=/home/noel/tmp go test ./internal/node/ -run TestMatchObserverSession_RejectsUnboundedGrant -v -count=1`
Expected: PASS

**Step 5: Run full node test suite**

Run: `TMPDIR=/home/noel/tmp go test ./internal/node/ -v -count=1`
Expected: PASS

**Step 6: Commit**

```bash
git add internal/node/node.go internal/node/observer_test.go
git commit -m "security: reject observer grants without session binding

Defense-in-depth: receiving nodes now independently verify that observer
delegation claims include at least session_id or session_name, rather
than trusting the signing function alone."
```

---

### Task 4: Validate X-Peer-URL header

**Files:**
- Modify: `internal/relay/node_handler.go:32`
- Modify: `internal/relay/node_handler.go` (add validation helper)

**Context:** `RegisterNodeConnectHandler` accepts the `X-CodeWire-Peer-URL` header at face value with only `strings.TrimSpace()`. A compromised node can advertise an arbitrary string as its peer URL, causing other nodes to connect to an attacker-controlled endpoint. While WireGuard key verification prevents content interception, it enables connection hijacking and DoS.

**Step 1: Write the failing test**

Add to an appropriate test file (or create `internal/relay/node_handler_test.go`):

```go
package relay

import (
	"testing"
)

func TestValidatePeerURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid https", "https://node1.example.com:8080", false},
		{"valid http", "http://192.168.1.1:9090", false},
		{"empty is ok", "", false},
		{"no scheme", "node1.example.com:8080", true},
		{"javascript scheme", "javascript:alert(1)", true},
		{"ftp scheme", "ftp://evil.com", true},
		{"too long", string(make([]byte, 513)), true},
		{"no host", "https://", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePeerURL(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("validatePeerURL(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}
```

**Step 2: Run test to verify it fails**

Run: `TMPDIR=/home/noel/tmp go test ./internal/relay/ -run TestValidatePeerURL -v -count=1`
Expected: compile error -- `validatePeerURL` doesn't exist.

**Step 3: Write minimal implementation**

Add to `internal/relay/node_handler.go`:

```go
func validatePeerURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if len(raw) > 512 {
		return fmt.Errorf("peer URL too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid peer URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("peer URL scheme must be http or https")
	}
	if u.Host == "" {
		return fmt.Errorf("peer URL must include host")
	}
	return nil
}
```

Add imports: `"fmt"`, `"net/url"`.

**Step 4: Wire validation into the handler**

In `RegisterNodeConnectHandler`, replace line 32:

```go
peerURL := strings.TrimSpace(r.Header.Get("X-CodeWire-Peer-URL"))
if err := validatePeerURL(peerURL); err != nil {
	slog.Warn("invalid peer URL from node", "node", node.Name, "peer_url", peerURL, "err", err)
	peerURL = ""
}
node.PeerURL = peerURL
```

This silently drops invalid URLs rather than rejecting the connection -- a compromised node's connectivity shouldn't break because of a bad URL, but we don't store the bad value.

**Step 5: Run test to verify it passes**

Run: `TMPDIR=/home/noel/tmp go test ./internal/relay/ -run TestValidatePeerURL -v -count=1`
Expected: PASS

**Step 6: Commit**

```bash
git add internal/relay/node_handler.go
git commit -m "security: validate X-CodeWire-Peer-URL header

Reject non-HTTP(S) schemes, overly long values, and missing hosts.
Invalid URLs are silently dropped rather than stored."
```

---

## Phase 2: Small-medium fixes

### Task 5: Rate limit ?role=dial coordinator registrations

**Files:**
- Modify: `internal/relay/tailnet.go:60-84`

**Context:** Any authenticated peer connecting with `?role=dial` gets a fresh `uuid.New()` on every connection (line 77), each allocating a `coordPeer` with a 64-slot buffered channel. No per-credential rate limit exists. A single compromised runtime credential can exhaust coordinator memory by opening thousands of connections.

The fix: track active dial connections per credential identity (SubjectKind+SubjectID+NetworkID) and reject new connections when the count exceeds a threshold.

**Step 1: Write the failing test**

Add to `internal/relay/tailnet_test.go` (create if needed):

```go
package relay

import (
	"sync"
	"testing"
)

func TestDialLimiter(t *testing.T) {
	lim := newDialLimiter(3)

	// First 3 should succeed
	for i := 0; i < 3; i++ {
		if !lim.acquire("net-1:client:user-1") {
			t.Fatalf("acquire %d should succeed", i)
		}
	}

	// 4th should fail
	if lim.acquire("net-1:client:user-1") {
		t.Fatal("acquire should fail at limit")
	}

	// Different identity should succeed
	if !lim.acquire("net-1:client:user-2") {
		t.Fatal("different identity should succeed")
	}

	// Release one, then acquire should work again
	lim.release("net-1:client:user-1")
	if !lim.acquire("net-1:client:user-1") {
		t.Fatal("acquire after release should succeed")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `TMPDIR=/home/noel/tmp go test ./internal/relay/ -run TestDialLimiter -v -count=1`
Expected: compile error -- `dialLimiter` doesn't exist.

**Step 3: Write minimal implementation**

Add to `internal/relay/tailnet.go`:

```go
type dialLimiter struct {
	mu       sync.Mutex
	maxPerID int
	active   map[string]int
}

func newDialLimiter(maxPerID int) *dialLimiter {
	return &dialLimiter{
		maxPerID: maxPerID,
		active:   make(map[string]int),
	}
}

func (l *dialLimiter) acquire(identity string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active[identity] >= l.maxPerID {
		return false
	}
	l.active[identity]++
	return true
}

func (l *dialLimiter) release(identity string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active[identity] > 0 {
		l.active[identity]--
	}
	if l.active[identity] == 0 {
		delete(l.active, identity)
	}
}
```

**Step 4: Run test to verify it passes**

Run: `TMPDIR=/home/noel/tmp go test ./internal/relay/ -run TestDialLimiter -v -count=1`
Expected: PASS

**Step 5: Wire into the coordinator handler**

In `tailnetCoordinateHandler`, the limiter must be created once and passed into the handler. In `internal/relay/relay.go` where `tailnetCoordinateHandler` is called from `buildMux`:

1. Create the limiter alongside the coordinator:
```go
dialLim := newDialLimiter(16) // 16 concurrent dial connections per credential
```

2. Pass it to the handler:
```go
mux.HandleFunc("GET /api/v1/tailnet/coordinate", tailnetCoordinateHandler(coord, st, replayCache, cfg, dialLim))
```

3. In `tailnetCoordinateHandler` signature, add the parameter and check after line 76:

```go
func tailnetCoordinateHandler(coord *tailnet.Coordinator, st store.Store, replayCache *networkauth.ReplayCache, cfg relayConfig, dialLim *dialLimiter) http.HandlerFunc {
```

Inside the handler, after `peerID = uuid.New()` (line 77):

```go
if claims.SubjectKind == networkauth.SubjectKindClient || r.URL.Query().Get("role") == "dial" {
	peerID = uuid.New()
	identity := claims.NetworkID + ":" + claims.SubjectKind + ":" + claims.SubjectID
	if !dialLim.acquire(identity) {
		wsConn.Close(websocket.StatusPolicyViolation, "too many dial connections")
		return
	}
	defer dialLim.release(identity)
}
```

**Step 6: Run relay tests**

Run: `TMPDIR=/home/noel/tmp go test ./internal/relay/ -v -count=1 -timeout 120s`
Expected: PASS

**Step 7: Commit**

```bash
git add internal/relay/tailnet.go internal/relay/relay.go
git commit -m "security: rate limit dial coordinator connections per credential

Each ?role=dial connection allocates a coordPeer with a buffered channel.
Without limits, a single credential can exhaust coordinator memory.
Caps concurrent dial connections at 16 per credential identity."
```

---

### Task 6: Atomic enrollment consumption

**Files:**
- Modify: `internal/store/sqlite.go:1009-1059`
- Modify: `internal/store/sqlite_test.go`

**Context:** `NodeEnrollmentConsume` does a SELECT then UPDATE in a transaction, but SQLite's default isolation doesn't prevent the TOCTOU issue in a multi-process scenario. The in-process mutex protects single-instance relay, but atomic SQL is strictly better and matches the pattern already used by `InviteConsume` (which uses `UPDATE ... WHERE uses_remaining > 0`).

**Step 1: Write the test**

Add to `internal/store/sqlite_test.go`:

```go
func TestNodeEnrollmentConsume_Atomic(t *testing.T) {
	s, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	now := time.Now().UTC()
	tokenHash := "abc123hash"
	enrollment := NodeEnrollment{
		ID:            "enr-1",
		NetworkID:     "net-1",
		OwnerSubject:  "user:1",
		IssuedBy:      "admin",
		NodeName:      "dev-1",
		TokenHash:     tokenHash,
		UsesRemaining: 1,
		ExpiresAt:     now.Add(time.Hour),
		CreatedAt:     now,
	}
	if err := s.NodeEnrollmentCreate(ctx, enrollment); err != nil {
		t.Fatalf("NodeEnrollmentCreate: %v", err)
	}

	// First consume should succeed
	got, err := s.NodeEnrollmentConsume(ctx, tokenHash, now)
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if got == nil {
		t.Fatal("first consume returned nil")
	}
	if got.UsesRemaining != 0 {
		t.Fatalf("UsesRemaining = %d, want 0", got.UsesRemaining)
	}

	// Second consume should return nil (no uses remaining)
	got2, err := s.NodeEnrollmentConsume(ctx, tokenHash, now)
	if err != nil {
		t.Fatalf("second consume: %v", err)
	}
	if got2 != nil {
		t.Fatal("second consume should return nil")
	}
}
```

**Step 2: Run test (should pass with current code -- this is a refactor)**

Run: `TMPDIR=/home/noel/tmp go test ./internal/store/ -run TestNodeEnrollmentConsume_Atomic -v -count=1`
Expected: PASS (the current implementation works for single-process; this test validates behavior before refactoring).

**Step 3: Refactor to atomic SQL**

Replace `NodeEnrollmentConsume` in `internal/store/sqlite.go` with:

```go
func (s *SQLiteStore) NodeEnrollmentConsume(_ context.Context, tokenHash string, redeemedAt time.Time) (*NodeEnrollment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Atomic decrement -- single statement prevents TOCTOU race.
	res, err := s.db.Exec(
		`UPDATE node_enrollments SET uses_remaining = uses_remaining - 1,
		 redeemed_at = CASE WHEN uses_remaining = 1 THEN ? ELSE redeemed_at END
		 WHERE token_hash = ? AND uses_remaining > 0 AND expires_at > ?`,
		redeemedAt, tokenHash, redeemedAt,
	)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, nil
	}

	// Read back the updated row.
	var e NodeEnrollment
	err = s.db.QueryRow(
		`SELECT id, network_id, owner_subject, issued_by, node_name, token_hash, uses_remaining, expires_at, created_at, redeemed_at
		 FROM node_enrollments WHERE token_hash = ?`,
		tokenHash,
	).Scan(&e.ID, &e.NetworkID, &e.OwnerSubject, &e.IssuedBy, &e.NodeName, &e.TokenHash, &e.UsesRemaining, &e.ExpiresAt, &e.CreatedAt, &e.RedeemedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}
```

**Step 4: Run test again**

Run: `TMPDIR=/home/noel/tmp go test ./internal/store/ -run TestNodeEnrollmentConsume_Atomic -v -count=1`
Expected: PASS

**Step 5: Run full store tests**

Run: `TMPDIR=/home/noel/tmp go test ./internal/store/ -v -count=1`
Expected: PASS

**Step 6: Commit**

```bash
git add internal/store/sqlite.go internal/store/sqlite_test.go
git commit -m "security: atomic enrollment consumption with single UPDATE

Replace SELECT-then-UPDATE pattern with atomic UPDATE...WHERE uses > 0.
Matches the existing InviteConsume pattern and prevents TOCTOU in
multi-process deployments."
```

---

## Phase 3: Medium effort, needs design

### Task 7: Persistent replay cache (SQLite-backed JTI table)

**Files:**
- Create: `internal/store/replay.go`
- Modify: `internal/store/store.go` (add interface methods)
- Modify: `internal/store/sqlite.go` (add table + implementation)
- Modify: `internal/networkauth/replay.go` (add Store-backed implementation)
- Modify: `internal/node/node.go:67-68` (use persistent cache)
- Modify: `internal/relay/tailnet.go` (use persistent cache on relay side)
- Modify: `internal/store/sqlite_test.go`

**Context:** The replay cache (`internal/networkauth/replay.go`) is an in-memory Go map. It's lost on process restart and not shared across nodes. A sender delegation consumed on node A can be replayed on node B. On restart, all previously-consumed JTIs become valid again within their expiry window (5 min default).

**Design decision:** Add a `consumed_jti` table to SQLite. The relay already runs SQLite; nodes don't have a relay-side store but they DO have local data directories. We add a lightweight SQLite-backed replay cache that wraps the existing in-memory cache with persistence.

For the relay coordinator (tailnet.go), the replay cache is already shared across all coordinated connections in-process. Persistence across restarts is the primary win.

For node-to-node replay (the cross-node problem), this is harder -- nodes are independent processes. The architectural options are:
- (a) Each node persists its own JTI table (prevents restart replay, not cross-node)
- (b) Relay-side JTI verification (relay checks before forwarding -- changes auth flow)

This task implements (a). Option (b) is a separate design discussion.

**Step 1: Add store interface methods**

Add to `internal/store/store.go` in the Store interface:

```go
// Replay cache -- JTI tracking for credential replay prevention.
ConsumedJTIAdd(ctx context.Context, kind, networkID, jti string, expiresAt time.Time) error
ConsumedJTIExists(ctx context.Context, kind, networkID, jti string) (bool, error)
ConsumedJTICleanup(ctx context.Context) error
```

**Step 2: Write the failing store test**

Add to `internal/store/sqlite_test.go`:

```go
func TestConsumedJTI(t *testing.T) {
	s, err := NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	now := time.Now().UTC()

	// Should not exist initially
	exists, err := s.ConsumedJTIExists(ctx, "runtime", "net-1", "jti-abc")
	if err != nil {
		t.Fatalf("ConsumedJTIExists: %v", err)
	}
	if exists {
		t.Fatal("JTI should not exist initially")
	}

	// Add it
	err = s.ConsumedJTIAdd(ctx, "runtime", "net-1", "jti-abc", now.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("ConsumedJTIAdd: %v", err)
	}

	// Should exist now
	exists, err = s.ConsumedJTIExists(ctx, "runtime", "net-1", "jti-abc")
	if err != nil {
		t.Fatalf("ConsumedJTIExists: %v", err)
	}
	if !exists {
		t.Fatal("JTI should exist after add")
	}

	// Duplicate add should fail (replay detection)
	err = s.ConsumedJTIAdd(ctx, "runtime", "net-1", "jti-abc", now.Add(5*time.Minute))
	if err == nil {
		t.Fatal("duplicate JTI add should fail")
	}

	// Add expired JTI, cleanup should remove it
	err = s.ConsumedJTIAdd(ctx, "runtime", "net-1", "jti-expired", now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("ConsumedJTIAdd expired: %v", err)
	}
	err = s.ConsumedJTICleanup(ctx)
	if err != nil {
		t.Fatalf("ConsumedJTICleanup: %v", err)
	}
	exists, err = s.ConsumedJTIExists(ctx, "runtime", "net-1", "jti-expired")
	if err != nil {
		t.Fatalf("ConsumedJTIExists after cleanup: %v", err)
	}
	if exists {
		t.Fatal("expired JTI should be cleaned up")
	}

	// Non-expired JTI should survive cleanup
	exists, err = s.ConsumedJTIExists(ctx, "runtime", "net-1", "jti-abc")
	if err != nil {
		t.Fatalf("ConsumedJTIExists after cleanup: %v", err)
	}
	if !exists {
		t.Fatal("non-expired JTI should survive cleanup")
	}
}
```

**Step 3: Run test to verify it fails**

Run: `TMPDIR=/home/noel/tmp go test ./internal/store/ -run TestConsumedJTI -v -count=1`
Expected: compile error -- methods don't exist on SQLiteStore.

**Step 4: Implement SQLite consumed_jti table**

Add table creation to `internal/store/sqlite.go` migrations:

```sql
CREATE TABLE IF NOT EXISTS consumed_jti (
	kind TEXT NOT NULL,
	network_id TEXT NOT NULL,
	jti TEXT NOT NULL,
	expires_at DATETIME NOT NULL,
	PRIMARY KEY (kind, network_id, jti)
)
```

Implement the three methods:

```go
func (s *SQLiteStore) ConsumedJTIAdd(_ context.Context, kind, networkID, jti string, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(
		"INSERT INTO consumed_jti (kind, network_id, jti, expires_at) VALUES (?, ?, ?, ?)",
		kind, networkID, jti, expiresAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("jti already consumed or storage error: %w", err)
	}
	return nil
}

func (s *SQLiteStore) ConsumedJTIExists(_ context.Context, kind, networkID, jti string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var count int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM consumed_jti WHERE kind = ? AND network_id = ? AND jti = ? AND expires_at > ?",
		kind, networkID, jti, time.Now().UTC(),
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *SQLiteStore) ConsumedJTICleanup(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("DELETE FROM consumed_jti WHERE expires_at <= ?", time.Now().UTC())
	return err
}
```

**Step 5: Run test to verify it passes**

Run: `TMPDIR=/home/noel/tmp go test ./internal/store/ -run TestConsumedJTI -v -count=1`
Expected: PASS

**Step 6: Create persistent ReplayCache wrapper**

Add `PersistentReplayCache` to `internal/networkauth/replay.go`:

```go
// JTIStore is the minimal interface for persistent JTI tracking.
type JTIStore interface {
	ConsumedJTIAdd(ctx context.Context, kind, networkID, jti string, expiresAt time.Time) error
	ConsumedJTIExists(ctx context.Context, kind, networkID, jti string) (bool, error)
}

// PersistentReplayCache checks both in-memory and persistent storage.
type PersistentReplayCache struct {
	mem   *ReplayCache
	store JTIStore
}

func NewPersistentReplayCache(store JTIStore) *PersistentReplayCache {
	return &PersistentReplayCache{
		mem:   NewReplayCache(),
		store: store,
	}
}

func (p *PersistentReplayCache) ConsumeRuntime(claims *RuntimeClaims, now time.Time) error {
	if claims == nil {
		return fmt.Errorf("runtime claims are nil")
	}
	return p.consume("runtime", claims.NetworkID, claims.JTI, claims.ExpiresAt, now)
}

func (p *PersistentReplayCache) ConsumeSender(claims *SenderDelegationClaims, now time.Time) error {
	if claims == nil {
		return fmt.Errorf("sender delegation claims are nil")
	}
	return p.consume("sender", claims.NetworkID, claims.JTI, claims.ExpiresAt, now)
}

func (p *PersistentReplayCache) ConsumeObserver(claims *ObserverDelegationClaims, now time.Time) error {
	if claims == nil {
		return fmt.Errorf("observer delegation claims are nil")
	}
	return p.consume("observer", claims.NetworkID, claims.JTI, claims.ExpiresAt, now)
}

func (p *PersistentReplayCache) consume(kind, networkID, jti string, expiresAt, now time.Time) error {
	// Check in-memory first (fast path).
	if err := p.mem.consume(kind, networkID, jti, expiresAt, now); err != nil {
		return err
	}
	// Persist to storage (catches restarts).
	ctx := context.Background()
	if exists, _ := p.store.ConsumedJTIExists(ctx, kind, ResolveNetworkID(networkID), strings.TrimSpace(jti)); exists {
		return fmt.Errorf("%s credential replay detected (persistent)", kind)
	}
	if err := p.store.ConsumedJTIAdd(ctx, kind, ResolveNetworkID(networkID), strings.TrimSpace(jti), expiresAt.UTC().Add(maxClockSkew)); err != nil {
		return fmt.Errorf("%s credential replay detected: %w", kind, err)
	}
	return nil
}
```

**Step 7: Write test for PersistentReplayCache**

Add to `internal/networkauth/runtime_test.go`:

```go
type mockJTIStore struct {
	consumed map[string]time.Time
}

func (m *mockJTIStore) ConsumedJTIAdd(_ context.Context, kind, networkID, jti string, expiresAt time.Time) error {
	key := kind + ":" + networkID + ":" + jti
	if _, ok := m.consumed[key]; ok {
		return fmt.Errorf("duplicate")
	}
	m.consumed[key] = expiresAt
	return nil
}

func (m *mockJTIStore) ConsumedJTIExists(_ context.Context, kind, networkID, jti string) (bool, error) {
	key := kind + ":" + networkID + ":" + jti
	exp, ok := m.consumed[key]
	if !ok {
		return false, nil
	}
	return time.Now().Before(exp), nil
}

func TestPersistentReplayCache_SurvivesRestart(t *testing.T) {
	store := &mockJTIStore{consumed: make(map[string]time.Time)}

	state, err := NewIssuerState("project-alpha")
	if err != nil {
		t.Fatalf("NewIssuerState: %v", err)
	}

	now := time.Now().UTC()
	_, claims, err := SignRuntimeCredential(state, SubjectKindClient, "github:1", now, time.Minute)
	if err != nil {
		t.Fatalf("SignRuntimeCredential: %v", err)
	}

	// First cache consumes it
	cache1 := NewPersistentReplayCache(store)
	if err := cache1.ConsumeRuntime(claims, now); err != nil {
		t.Fatalf("first consume: %v", err)
	}

	// "Restart" -- new in-memory cache, same store
	cache2 := NewPersistentReplayCache(store)
	err = cache2.ConsumeRuntime(claims, now)
	if err == nil {
		t.Fatal("expected replay detection after restart, got nil")
	}
}
```

**Step 8: Run test**

Run: `TMPDIR=/home/noel/tmp go test ./internal/networkauth/ -run TestPersistentReplayCache_SurvivesRestart -v -count=1`
Expected: PASS

**Step 9: Wire into node initialization**

In `internal/node/node.go`, the Node currently creates `networkauth.NewReplayCache()` (lines 67-68). Nodes don't have a relay Store, so we need a lightweight local SQLite for JTI persistence. This is a design decision point:

- **Option A:** Open a small local SQLite in the node's data directory just for JTI tracking.
- **Option B:** Keep in-memory on nodes, only persist on relay.

For now, implement Option B (relay-only persistence) since the relay is the central trust point and has the existing SQLite store. Nodes keep in-memory caches. The relay's coordinator replay cache (`verifyRelayRuntimeCredential` in `tailnet.go`) gets persistence.

Update `internal/relay/tailnet.go` where `verifyRelayRuntimeCredential` is called -- pass a `PersistentReplayCache` instead of `ReplayCache`. This requires the relay to create the persistent cache during initialization.

In `internal/relay/relay.go` `buildMux` or `runRelay`:

```go
relayReplayCache := networkauth.NewPersistentReplayCache(st)
```

Pass `relayReplayCache` where the current `replayCache` is used.

**Step 10: Run full test suite**

Run: `TMPDIR=/home/noel/tmp go test ./internal/... -v -count=1 -timeout 120s`
Expected: PASS

**Step 11: Commit**

```bash
git add internal/store/ internal/networkauth/ internal/relay/ internal/node/
git commit -m "security: persistent replay cache backed by SQLite

JTIs consumed by the relay are now persisted to a consumed_jti table.
Relay restart no longer clears the replay window. Nodes keep in-memory
caches for now; relay-side persistence is the primary defense."
```

---

## Deferred (requires separate design discussion)

These items were identified in the security analysis but are not included in this plan because they require architectural decisions:

- **Issuer key encryption at rest (#3/#6):** Consider Infisical integration vs app-layer encryption. Threat model question: if attacker has SQLite, do they also have process memory?
- **Relay-side JTI verification (#1 cross-node):** Would prevent cross-node delegation replay but changes the auth flow. Sender delegations would need to be validated by the relay before forwarding.
- **Session revocation endpoint (#12):** The relay needs a `DELETE /api/v1/sessions/{token}` endpoint or `POST /auth/logout`. Straightforward but needs UX design (redirect target, cookie clearing).
- **SameSite: Strict (#10):** Low risk change but may break legitimate cross-origin flows (e.g., OAuth redirects from the IdP). Needs testing.
- **Clock skew reduction (#11):** Reduce `maxClockSkew` from 30s to 10s. Simple constant change but needs validation that deployed nodes have adequate NTP sync.
- **Grant revocation consistency (#13):** Nodes should re-validate grants against the relay on sensitive operations. Requires defining which operations are "sensitive" and acceptable latency budget.

---

Plan complete and saved to `docs/plans/2026-04-07-relay-security-hardening.md`. Two execution options:

**1. Subagent-Driven (this session)** - I dispatch fresh subagent per task, review between tasks, fast iteration

**2. Parallel Session (separate)** - Open new session with executing-plans, batch execution with checkpoints

Which approach?