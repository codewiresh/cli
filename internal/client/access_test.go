package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAccessGrantClientLifecycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	origClient := relayHTTPClient
	defer func() { relayHTTPClient = origClient }()

	var (
		sawCreate bool
		sawGet    bool
		sawList   bool
		sawRevoke bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer dev-secret" {
			t.Fatalf("Authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/access-grants":
			sawCreate = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("Decode create: %v", err)
			}
			if body["network_id"] != "project-alpha" {
				t.Fatalf("network_id = %#v", body["network_id"])
			}
			if body["target_node"] != "dev-2" {
				t.Fatalf("target_node = %#v", body["target_node"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"delegation":            "cwog1.payload.sig",
				"grant_id":              "og_123",
				"network_id":            "project-alpha",
				"target_node":           "dev-2",
				"session_name":          "coder",
				"verbs":                 []string{"msg.read", "msg.listen"},
				"audience_subject_kind": "client",
				"audience_subject_id":   "github:303",
				"audience_display":      "alice",
				"expires_at":            time.Now().UTC().Add(10 * time.Minute),
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/access-grants":
			sawList = true
			if got := r.URL.Query().Get("network_id"); got != "project-alpha" {
				t.Fatalf("list network_id = %q", got)
			}
			if got := r.URL.Query().Get("mine"); got != "true" {
				t.Fatalf("mine = %q", got)
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":                    "og_123",
				"network_id":            "project-alpha",
				"target_node":           "dev-2",
				"session_name":          "coder",
				"verbs":                 []string{"msg.read", "msg.listen"},
				"audience_subject_kind": "client",
				"audience_subject_id":   "github:303",
				"audience_display":      "alice",
				"created_at":            time.Now().UTC(),
				"expires_at":            time.Now().UTC().Add(10 * time.Minute),
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/access-grants/og_123":
			sawGet = true
			if got := r.URL.Query().Get("network_id"); got != "project-alpha" {
				t.Fatalf("get network_id = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":                    "og_123",
				"network_id":            "project-alpha",
				"target_node":           "dev-2",
				"session_name":          "coder",
				"verbs":                 []string{"msg.read", "msg.listen"},
				"audience_subject_kind": "client",
				"audience_subject_id":   "github:303",
				"audience_display":      "alice",
				"created_at":            time.Now().UTC(),
				"expires_at":            time.Now().UTC().Add(10 * time.Minute),
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/access-grants/og_123":
			sawRevoke = true
			if got := r.URL.Query().Get("network_id"); got != "project-alpha" {
				t.Fatalf("revoke network_id = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "revoked"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	relayHTTPClient = srv.Client()

	issued, err := CreateAccessGrant(t.TempDir(), RelayAuthOptions{
		RelayURL:  srv.URL,
		AuthToken: "dev-secret",
		NetworkID: "project-alpha",
	}, CreateAccessGrantOptions{
		TargetNode:  "dev-2",
		SessionName: "coder",
		Audience:    "alice",
		TTL:         "10m",
	})
	if err != nil {
		t.Fatalf("CreateAccessGrant: %v", err)
	}
	if issued.GrantID != "og_123" {
		t.Fatalf("GrantID = %q", issued.GrantID)
	}

	grants, err := ListAccessGrants(t.TempDir(), RelayAuthOptions{
		RelayURL:  srv.URL,
		AuthToken: "dev-secret",
		NetworkID: "project-alpha",
	}, ListAccessGrantOptions{ActiveOnly: true, Mine: true})
	if err != nil {
		t.Fatalf("ListAccessGrants: %v", err)
	}
	if len(grants) != 1 || grants[0].ID != "og_123" {
		t.Fatalf("grants = %#v", grants)
	}

	grant, err := GetAccessGrant(t.TempDir(), "og_123", RelayAuthOptions{
		RelayURL:  srv.URL,
		AuthToken: "dev-secret",
		NetworkID: "project-alpha",
	})
	if err != nil {
		t.Fatalf("GetAccessGrant: %v", err)
	}
	if grant.ID != "og_123" {
		t.Fatalf("grant = %#v", grant)
	}

	if err := RevokeAccessGrant(t.TempDir(), "og_123", RelayAuthOptions{
		RelayURL:  srv.URL,
		AuthToken: "dev-secret",
		NetworkID: "project-alpha",
	}); err != nil {
		t.Fatalf("RevokeAccessGrant: %v", err)
	}

	if !sawCreate || !sawGet || !sawList || !sawRevoke {
		t.Fatalf("sawCreate=%v sawGet=%v sawList=%v sawRevoke=%v", sawCreate, sawGet, sawList, sawRevoke)
	}
}
