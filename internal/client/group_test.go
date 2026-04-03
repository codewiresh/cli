package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGroupClientLifecycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	origClient := relayHTTPClient
	defer func() { relayHTTPClient = origClient }()

	var (
		sawCreate bool
		sawList   bool
		sawGet    bool
		sawAdd    bool
		sawRemove bool
		sawPolicy bool
		sawDelete bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer dev-secret" {
			t.Fatalf("Authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/groups":
			sawCreate = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("Decode create: %v", err)
			}
			if body["network_id"] != "project-alpha" || body["name"] != "mesh" {
				t.Fatalf("create body = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"network_id": "project-alpha",
				"name":       "mesh",
				"created_at": time.Now().UTC(),
				"policy": map[string]any{
					"network_id":      "project-alpha",
					"group_name":      "mesh",
					"messages_policy": "internal-only",
					"debug_policy":    "observe-only",
					"updated_at":      time.Now().UTC(),
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups":
			sawList = true
			if got := r.URL.Query().Get("network_id"); got != "project-alpha" {
				t.Fatalf("list network_id = %q", got)
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"network_id": "project-alpha",
				"name":       "mesh",
				"created_at": time.Now().UTC(),
				"policy": map[string]any{
					"network_id":      "project-alpha",
					"group_name":      "mesh",
					"messages_policy": "internal-only",
					"debug_policy":    "observe-only",
					"updated_at":      time.Now().UTC(),
				},
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/groups/mesh":
			sawGet = true
			if got := r.URL.Query().Get("network_id"); got != "project-alpha" {
				t.Fatalf("get network_id = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"network_id": "project-alpha",
				"name":       "mesh",
				"created_at": time.Now().UTC(),
				"members": []map[string]any{{
					"network_id":   "project-alpha",
					"group_name":   "mesh",
					"node_name":    "node-a",
					"session_name": "agent-1",
					"created_at":   time.Now().UTC(),
				}},
				"policy": map[string]any{
					"network_id":      "project-alpha",
					"group_name":      "mesh",
					"messages_policy": "internal-only",
					"debug_policy":    "observe-only",
					"updated_at":      time.Now().UTC(),
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/groups/mesh/members":
			sawAdd = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("Decode add member: %v", err)
			}
			if body["node_name"] != "node-a" || body["session_name"] != "agent-1" {
				t.Fatalf("add member body = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"network_id":   "project-alpha",
				"group_name":   "mesh",
				"node_name":    "node-a",
				"session_name": "agent-1",
				"created_at":   time.Now().UTC(),
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/groups/mesh/members":
			sawRemove = true
			if got := r.URL.Query().Get("network_id"); got != "project-alpha" {
				t.Fatalf("remove network_id = %q", got)
			}
			if got := r.URL.Query().Get("node_name"); got != "node-a" {
				t.Fatalf("remove node_name = %q", got)
			}
			if got := r.URL.Query().Get("session_name"); got != "agent-1" {
				t.Fatalf("remove session_name = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "removed"})
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/groups/mesh/policy":
			sawPolicy = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("Decode set policy: %v", err)
			}
			if body["messages_policy"] != "open" || body["debug_policy"] != "none" {
				t.Fatalf("policy body = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"network_id":      "project-alpha",
				"group_name":      "mesh",
				"messages_policy": "open",
				"debug_policy":    "none",
				"updated_at":      time.Now().UTC(),
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/groups/mesh":
			sawDelete = true
			if got := r.URL.Query().Get("network_id"); got != "project-alpha" {
				t.Fatalf("delete network_id = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	relayHTTPClient = srv.Client()

	group, err := CreateGroup(t.TempDir(), "mesh", RelayAuthOptions{
		RelayURL:  srv.URL,
		AuthToken: "dev-secret",
		NetworkID: "project-alpha",
	})
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if group.Name != "mesh" {
		t.Fatalf("group = %#v", group)
	}

	groups, err := ListGroups(t.TempDir(), RelayAuthOptions{
		RelayURL:  srv.URL,
		AuthToken: "dev-secret",
		NetworkID: "project-alpha",
	})
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(groups) != 1 || groups[0].Name != "mesh" {
		t.Fatalf("groups = %#v", groups)
	}

	group, err = GetGroup(t.TempDir(), "mesh", RelayAuthOptions{
		RelayURL:  srv.URL,
		AuthToken: "dev-secret",
		NetworkID: "project-alpha",
	})
	if err != nil {
		t.Fatalf("GetGroup: %v", err)
	}
	if len(group.Members) != 1 || group.Members[0].NodeName != "node-a" {
		t.Fatalf("group members = %#v", group.Members)
	}

	member, err := AddGroupMember(t.TempDir(), "mesh", "node-a", "agent-1", RelayAuthOptions{
		RelayURL:  srv.URL,
		AuthToken: "dev-secret",
		NetworkID: "project-alpha",
	})
	if err != nil {
		t.Fatalf("AddGroupMember: %v", err)
	}
	if member.NodeName != "node-a" || member.SessionName != "agent-1" {
		t.Fatalf("member = %#v", member)
	}

	policy, err := SetGroupPolicy(t.TempDir(), "mesh", RelayAuthOptions{
		RelayURL:  srv.URL,
		AuthToken: "dev-secret",
		NetworkID: "project-alpha",
	}, GroupPolicyUpdateOptions{
		MessagesPolicy: "open",
		DebugPolicy:    "none",
	})
	if err != nil {
		t.Fatalf("SetGroupPolicy: %v", err)
	}
	if policy.MessagesPolicy != "open" || policy.DebugPolicy != "none" {
		t.Fatalf("policy = %#v", policy)
	}

	if err := RemoveGroupMember(t.TempDir(), "mesh", "node-a", "agent-1", RelayAuthOptions{
		RelayURL:  srv.URL,
		AuthToken: "dev-secret",
		NetworkID: "project-alpha",
	}); err != nil {
		t.Fatalf("RemoveGroupMember: %v", err)
	}

	if err := DeleteGroup(t.TempDir(), "mesh", RelayAuthOptions{
		RelayURL:  srv.URL,
		AuthToken: "dev-secret",
		NetworkID: "project-alpha",
	}); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}

	if !sawCreate || !sawList || !sawGet || !sawAdd || !sawRemove || !sawPolicy || !sawDelete {
		t.Fatalf("sawCreate=%v sawList=%v sawGet=%v sawAdd=%v sawRemove=%v sawPolicy=%v sawDelete=%v", sawCreate, sawList, sawGet, sawAdd, sawRemove, sawPolicy, sawDelete)
	}
}
