package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/codewiresh/codewire/internal/networkauth"
	"github.com/codewiresh/codewire/internal/store"
)

func TestGroupLifecycleRequiresOwnerAndPersistsDefaults(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	ownerToken := createGitHubSession(t, st, 101, "owner")
	outsiderToken := createGitHubSession(t, st, 202, "outsider")
	now := time.Now().UTC()
	if err := st.NetworkMemberUpsert(context.Background(), store.NetworkMember{
		NetworkID: "project-alpha",
		Subject:   "github:101",
		Role:      store.NetworkRoleOwner,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("NetworkMemberUpsert: %v", err)
	}

	srv := newIPv4TestServer(t, buildMux(NewNodeHub(), NewPendingSessions(), st, RelayConfig{
		BaseURL:   "http://relay.test",
		AuthMode:  "token",
		AuthToken: "relay-admin",
	}, nil, networkauth.NewReplayCache(), nil))
	defer srv.Close()

	createBody, _ := json.Marshal(map[string]any{
		"network_id": "project-alpha",
		"name":       "mesh",
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/groups", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+outsiderToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("outsider create group: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("outsider create status = %d, want 403", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/v1/groups", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("owner create group: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner create status = %d", resp.StatusCode)
	}
	var created groupResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("Decode create response: %v", err)
	}
	if created.Name != "mesh" {
		t.Fatalf("created group = %#v", created)
	}
	if created.Policy == nil {
		t.Fatal("expected default group policy")
	}
	if created.Policy.MessagesPolicy != store.GroupMessagesInternalOnly {
		t.Fatalf("default messages policy = %q", created.Policy.MessagesPolicy)
	}
	if created.Policy.DebugPolicy != store.GroupDebugObserveOnly {
		t.Fatalf("default debug policy = %q", created.Policy.DebugPolicy)
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/groups?network_id=project-alpha", nil)
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("list groups: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list groups status = %d", resp.StatusCode)
	}
	var groups []groupResponse
	if err := json.NewDecoder(resp.Body).Decode(&groups); err != nil {
		t.Fatalf("Decode list response: %v", err)
	}
	if len(groups) != 1 || groups[0].Name != "mesh" {
		t.Fatalf("groups = %#v", groups)
	}

	memberBody, _ := json.Marshal(map[string]any{
		"network_id":   "project-alpha",
		"node_name":    "node-a",
		"session_name": "agent-1",
	})
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/v1/groups/mesh/members", bytes.NewReader(memberBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("add group member: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add group member status = %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/groups/mesh?network_id=project-alpha", nil)
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("get group: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get group status = %d", resp.StatusCode)
	}
	var group groupResponse
	if err := json.NewDecoder(resp.Body).Decode(&group); err != nil {
		t.Fatalf("Decode get response: %v", err)
	}
	if len(group.Members) != 1 {
		t.Fatalf("group members = %#v", group.Members)
	}
	if group.Members[0].NodeName != "node-a" || group.Members[0].SessionName != "agent-1" {
		t.Fatalf("group member = %#v", group.Members[0])
	}

	policyBody, _ := json.Marshal(map[string]any{
		"network_id":      "project-alpha",
		"messages_policy": "open",
		"debug_policy":    "none",
	})
	req, _ = http.NewRequest(http.MethodPut, srv.URL+"/api/v1/groups/mesh/policy", bytes.NewReader(policyBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("set group policy: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set group policy status = %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/groups/mesh?network_id=project-alpha", nil)
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("get group after policy update: %v", err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&group); err != nil {
		t.Fatalf("Decode updated group: %v", err)
	}
	if group.Policy == nil || group.Policy.MessagesPolicy != store.GroupMessagesOpen || group.Policy.DebugPolicy != store.GroupDebugNone {
		t.Fatalf("updated group policy = %#v", group.Policy)
	}

	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/groups/mesh/members?network_id=project-alpha&node_name=node-a&session_name=agent-1", nil)
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("remove group member: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("remove group member status = %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/groups/mesh?network_id=project-alpha", nil)
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("delete group: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete group status = %d", resp.StatusCode)
	}
}

func TestGroupHandlersRejectInvalidPolicyAndDuplicateCreate(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	ownerToken := createGitHubSession(t, st, 101, "owner")
	now := time.Now().UTC()
	if err := st.NetworkMemberUpsert(context.Background(), store.NetworkMember{
		NetworkID: "project-alpha",
		Subject:   "github:101",
		Role:      store.NetworkRoleOwner,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("NetworkMemberUpsert: %v", err)
	}

	srv := newIPv4TestServer(t, buildMux(NewNodeHub(), NewPendingSessions(), st, RelayConfig{
		BaseURL:   "http://relay.test",
		AuthMode:  "token",
		AuthToken: "relay-admin",
	}, nil, networkauth.NewReplayCache(), nil))
	defer srv.Close()

	createBody, _ := json.Marshal(map[string]any{
		"network_id": "project-alpha",
		"name":       "mesh",
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/groups", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create group status = %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/v1/groups", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("duplicate create group: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate create status = %d, want 409", resp.StatusCode)
	}

	policyBody, _ := json.Marshal(map[string]any{
		"network_id":      "project-alpha",
		"messages_policy": "bad-value",
		"debug_policy":    "observe-only",
	})
	req, _ = http.NewRequest(http.MethodPut, srv.URL+"/api/v1/groups/mesh/policy", bytes.NewReader(policyBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("invalid policy request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid policy status = %d, want 400", resp.StatusCode)
	}
}

func TestGroupBindingsHandlerAllowsNodeScopedLookup(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()
	if err := st.NodeRegister(context.Background(), store.NodeRecord{
		NetworkID:    "project-alpha",
		Name:         "node-a",
		Token:        "node-token",
		AuthorizedAt: now,
		LastSeenAt:   now,
	}); err != nil {
		t.Fatalf("NodeRegister: %v", err)
	}
	if err := st.GroupCreate(context.Background(), store.Group{
		NetworkID: "project-alpha",
		Name:      "mesh",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("GroupCreate: %v", err)
	}
	if err := st.GroupMemberAdd(context.Background(), store.GroupMember{
		NetworkID:   "project-alpha",
		GroupName:   "mesh",
		NodeName:    "node-a",
		SessionName: "agent-1",
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("GroupMemberAdd: %v", err)
	}

	srv := newIPv4TestServer(t, buildMux(NewNodeHub(), NewPendingSessions(), st, RelayConfig{
		BaseURL: "http://relay.test",
	}, nil, networkauth.NewReplayCache(), nil))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/groups/bindings?session_name=agent-1", nil)
	req.Header.Set("Authorization", "Bearer node-token")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("node bindings lookup: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("node bindings status = %d", resp.StatusCode)
	}

	var bindings []store.GroupBinding
	if err := json.NewDecoder(resp.Body).Decode(&bindings); err != nil {
		t.Fatalf("Decode bindings: %v", err)
	}
	if len(bindings) != 1 || bindings[0].GroupName != "mesh" {
		t.Fatalf("bindings = %#v", bindings)
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/groups/bindings?node_name=node-b&session_name=agent-1", nil)
	req.Header.Set("Authorization", "Bearer node-token")
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("cross-node bindings lookup: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-node bindings status = %d, want 403", resp.StatusCode)
	}
}

func TestGroupMemberHandlersAllowNodeScopedMutation(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()
	if err := st.NodeRegister(context.Background(), store.NodeRecord{
		NetworkID:    "project-alpha",
		Name:         "node-a",
		Token:        "node-token",
		AuthorizedAt: now,
		LastSeenAt:   now,
	}); err != nil {
		t.Fatalf("NodeRegister: %v", err)
	}
	if err := st.GroupCreate(context.Background(), store.Group{
		NetworkID: "project-alpha",
		Name:      "mesh",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("GroupCreate: %v", err)
	}

	srv := newIPv4TestServer(t, buildMux(NewNodeHub(), NewPendingSessions(), st, RelayConfig{
		BaseURL: "http://relay.test",
	}, nil, networkauth.NewReplayCache(), nil))
	defer srv.Close()

	addBody, _ := json.Marshal(map[string]any{
		"session_name": "agent-1",
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/groups/mesh/members", bytes.NewReader(addBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer node-token")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("node add group member: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("node add group member status = %d", resp.StatusCode)
	}

	members, err := st.GroupMemberList(context.Background(), "project-alpha", "mesh")
	if err != nil {
		t.Fatalf("GroupMemberList: %v", err)
	}
	if len(members) != 1 || members[0].NodeName != "node-a" || members[0].SessionName != "agent-1" {
		t.Fatalf("members = %#v", members)
	}

	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/groups/mesh/members?session_name=agent-1", nil)
	req.Header.Set("Authorization", "Bearer node-token")
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("node remove group member: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("node remove group member status = %d", resp.StatusCode)
	}

	forbiddenBody, _ := json.Marshal(map[string]any{
		"node_name":    "node-b",
		"session_name": "agent-1",
	})
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/v1/groups/mesh/members", bytes.NewReader(forbiddenBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer node-token")
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("cross-node add group member: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-node add group member status = %d, want 403", resp.StatusCode)
	}
}
