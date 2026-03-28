package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterWithAuthToken(t *testing.T) {
	sawAuth := ""
	sawNetwork := ""
	sawNode := ""
	sawRedeemNode := ""

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/node-enrollments":
			if r.Method != http.MethodPost {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			sawAuth = r.Header.Get("Authorization")
			var req struct {
				NodeName  string `json:"node_name"`
				NetworkID string `json:"network_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("Decode: %v", err)
			}
			sawNode = req.NodeName
			sawNetwork = req.NetworkID
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"enrollment_token": "enroll-token"})
		case "/api/v1/node-enrollments/redeem":
			if r.Method != http.MethodPost {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			var req struct {
				NodeName        string `json:"node_name"`
				EnrollmentToken string `json:"enrollment_token"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("Decode: %v", err)
			}
			sawRedeemNode = req.NodeName
			if req.EnrollmentToken != "enroll-token" {
				t.Fatalf("enrollment_token = %q", req.EnrollmentToken)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"node_token": "node-token"})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	nodeToken, err := RegisterWithAuthToken(context.Background(), srv.URL, "project-alpha", "dev-1", "sess-token")
	if err != nil {
		t.Fatalf("RegisterWithAuthToken: %v", err)
	}
	if nodeToken != "node-token" {
		t.Fatalf("nodeToken = %q, want node-token", nodeToken)
	}
	if sawAuth != "Bearer sess-token" {
		t.Fatalf("Authorization = %q", sawAuth)
	}
	if sawNode != "dev-1" {
		t.Fatalf("node_name = %q", sawNode)
	}
	if sawRedeemNode != "dev-1" {
		t.Fatalf("redeem node_name = %q", sawRedeemNode)
	}
	if sawNetwork != "project-alpha" {
		t.Fatalf("network_id = %q", sawNetwork)
	}
}

func TestCreateNodeEnrollment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/node-enrollments" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sess-token" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":               "enr_123",
			"network_id":       "project-alpha",
			"node_name":        "dev-1",
			"uses_remaining":   1,
			"enrollment_token": "cw_enr_token",
		})
	}))
	defer srv.Close()

	enrollment, err := CreateNodeEnrollment(context.Background(), srv.URL, "project-alpha", "dev-1", "sess-token", 1, "10m")
	if err != nil {
		t.Fatalf("CreateNodeEnrollment: %v", err)
	}
	if enrollment.EnrollmentToken != "cw_enr_token" {
		t.Fatalf("EnrollmentToken = %q", enrollment.EnrollmentToken)
	}
}

func TestRedeemNodeEnrollment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/node-enrollments/redeem" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		var req struct {
			NodeName        string `json:"node_name"`
			EnrollmentToken string `json:"enrollment_token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if req.NodeName != "dev-1" || req.EnrollmentToken != "cw_enr_token" {
			t.Fatalf("redeem request = %+v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"node_token": "node-token"})
	}))
	defer srv.Close()

	redeemed, err := RedeemNodeEnrollment(context.Background(), srv.URL, "cw_enr_token", "dev-1")
	if err != nil {
		t.Fatalf("RedeemNodeEnrollment: %v", err)
	}
	if redeemed.NodeToken != "node-token" {
		t.Fatalf("nodeToken = %q, want node-token", redeemed.NodeToken)
	}
}

func TestJoinNetworkWithInvite(t *testing.T) {
	sawAuth := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/networks/join" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		sawAuth = r.Header.Get("Authorization")
		var req struct {
			InviteToken string `json:"invite_token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if req.InviteToken != "CW-INV-TEST" {
			t.Fatalf("invite_token = %q", req.InviteToken)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"network_id": "project-alpha",
		})
	}))
	defer srv.Close()

	result, err := JoinNetworkWithInvite(context.Background(), srv.URL, "sess-token", "CW-INV-TEST")
	if err != nil {
		t.Fatalf("JoinNetworkWithInvite: %v", err)
	}
	if sawAuth != "Bearer sess-token" {
		t.Fatalf("Authorization = %q", sawAuth)
	}
	if result.NetworkID != "project-alpha" {
		t.Fatalf("NetworkID = %q", result.NetworkID)
	}
}

func TestSSHURIIncludesNetworkPrefix(t *testing.T) {
	got := SSHURI("https://relay.example.com", "network-alpha", "builder", "node-token", 2222)
	want := "ssh://network-alpha/builder:node-token@relay.example.com:2222"
	if got != want {
		t.Fatalf("SSHURI = %q, want %q", got, want)
	}
}
