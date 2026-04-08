package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/codewiresh/codewire/internal/networkauth"
	"github.com/codewiresh/codewire/internal/store"
)

func TestNetworkAuthClientRuntimeCredential(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	handler := buildMux(NewNodeHub(), NewPendingSessions(), st, RelayConfig{
		AuthMode:  "token",
		AuthToken: "relay-admin",
	}, nil, networkauth.NewReplayCache(), nil)
	srv := newIPv4TestServer(t, handler)
	defer srv.Close()

	issued, err := networkauth.IssueClientRuntimeCredential(context.Background(), srv.Client(), srv.URL, "relay-admin", "project-alpha")
	if err != nil {
		t.Fatalf("IssueClientRuntimeCredential: %v", err)
	}
	if issued.SubjectKind != networkauth.SubjectKindClient {
		t.Fatalf("SubjectKind = %q", issued.SubjectKind)
	}

	bundle, err := networkauth.FetchVerifierBundleWithToken(context.Background(), srv.Client(), srv.URL, "project-alpha", "relay-admin")
	if err != nil {
		t.Fatalf("FetchVerifierBundleWithToken: %v", err)
	}

	claims, err := networkauth.VerifyRuntimeCredential(issued.Credential, bundle, time.Now().UTC())
	if err != nil {
		t.Fatalf("VerifyRuntimeCredential: %v", err)
	}
	if claims.NetworkID != "project-alpha" {
		t.Fatalf("NetworkID = %q", claims.NetworkID)
	}
	if claims.SubjectID != "admin" {
		t.Fatalf("SubjectID = %q, want admin", claims.SubjectID)
	}
}

func TestNetworkAuthNodeRuntimeCredential(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()
	if err := st.NodeRegister(context.Background(), store.NodeRecord{
		NetworkID:    "project-alpha",
		Name:         "dev-2",
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
		NodeName:    "dev-1",
		SessionName: "planner",
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("GroupMemberAdd: %v", err)
	}

	handler := buildMux(NewNodeHub(), NewPendingSessions(), st, RelayConfig{}, nil, networkauth.NewReplayCache(), nil)
	srv := newIPv4TestServer(t, handler)
	defer srv.Close()

	issued, err := networkauth.IssueNodeRuntimeCredential(context.Background(), srv.Client(), srv.URL, "node-token")
	if err != nil {
		t.Fatalf("IssueNodeRuntimeCredential: %v", err)
	}
	if issued.SubjectKind != networkauth.SubjectKindNode {
		t.Fatalf("SubjectKind = %q", issued.SubjectKind)
	}
	if issued.SubjectID != "dev-2" {
		t.Fatalf("SubjectID = %q", issued.SubjectID)
	}

	bundle, err := networkauth.FetchVerifierBundleWithToken(context.Background(), srv.Client(), srv.URL, "project-alpha", "node-token")
	if err != nil {
		t.Fatalf("FetchVerifierBundleWithToken: %v", err)
	}

	claims, err := networkauth.VerifyRuntimeCredential(issued.Credential, bundle, time.Now().UTC())
	if err != nil {
		t.Fatalf("VerifyRuntimeCredential: %v", err)
	}
	if claims.SubjectID != "dev-2" {
		t.Fatalf("verified SubjectID = %q", claims.SubjectID)
	}
}

func TestNetworkAuthNodeSenderDelegation(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()
	if err := st.NodeRegister(context.Background(), store.NodeRecord{
		NetworkID:    "project-alpha",
		Name:         "dev-1",
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
		NodeName:    "dev-1",
		SessionName: "planner",
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("GroupMemberAdd: %v", err)
	}

	handler := buildMux(NewNodeHub(), NewPendingSessions(), st, RelayConfig{}, nil, networkauth.NewReplayCache(), nil)
	srv := newIPv4TestServer(t, handler)
	defer srv.Close()

	sessionID := uint32(42)
	issued, err := networkauth.IssueNodeSenderDelegation(context.Background(), srv.Client(), srv.URL, "node-token", "dev-1", &sessionID, "planner", []string{"msg"}, "dev-2")
	if err != nil {
		t.Fatalf("IssueNodeSenderDelegation: %v", err)
	}
	if issued.SourceNode != "dev-1" {
		t.Fatalf("SourceNode = %q", issued.SourceNode)
	}

	bundle, err := networkauth.FetchVerifierBundleWithToken(context.Background(), srv.Client(), srv.URL, "project-alpha", "node-token")
	if err != nil {
		t.Fatalf("FetchVerifierBundleWithToken: %v", err)
	}

	claims, err := networkauth.VerifySenderDelegation(issued.Delegation, bundle, time.Now().UTC())
	if err != nil {
		t.Fatalf("VerifySenderDelegation: %v", err)
	}
	if claims.FromSessionName != "planner" {
		t.Fatalf("FromSessionName = %q", claims.FromSessionName)
	}
	if claims.AudienceNode != "dev-2" {
		t.Fatalf("AudienceNode = %q", claims.AudienceNode)
	}
	if len(claims.SourceGroups) != 1 || claims.SourceGroups[0] != "mesh" {
		t.Fatalf("SourceGroups = %#v", claims.SourceGroups)
	}
}

func TestVerifierBundleStableAcrossRequests(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()
	if err := st.NodeRegister(context.Background(), store.NodeRecord{
		NetworkID:    "project-alpha",
		Name:         "dev-2",
		Token:        "node-token",
		AuthorizedAt: now,
		LastSeenAt:   now,
	}); err != nil {
		t.Fatalf("NodeRegister: %v", err)
	}

	handler := buildMux(NewNodeHub(), NewPendingSessions(), st, RelayConfig{}, nil, networkauth.NewReplayCache(), nil)
	srv := newIPv4TestServer(t, handler)
	defer srv.Close()

	if _, err := networkauth.IssueNodeRuntimeCredential(context.Background(), srv.Client(), srv.URL, "node-token"); err != nil {
		t.Fatalf("IssueNodeRuntimeCredential: %v", err)
	}

	for i := 0; i < 2; i++ {
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/network-auth/bundle?network_id=project-alpha", nil)
		if err != nil {
			t.Fatalf("NewRequest bundle: %v", err)
		}
		req.Header.Set("Authorization", "Bearer node-token")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("GET bundle: %v", err)
		}
		defer resp.Body.Close()
		var bundle networkauth.VerifierBundle
		if err := json.NewDecoder(resp.Body).Decode(&bundle); err != nil {
			t.Fatalf("Decode bundle: %v", err)
		}
		if len(bundle.Keys) != 1 {
			t.Fatalf("Keys len = %d, want 1", len(bundle.Keys))
		}
	}
}

func TestVerifierBundleRequiresAuthorizedCaller(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	memberToken := createGitHubSession(t, st, 101, "member")
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

	handler := buildMux(NewNodeHub(), NewPendingSessions(), st, RelayConfig{}, nil, networkauth.NewReplayCache(), nil)
	srv := newIPv4TestServer(t, handler)
	defer srv.Close()

	if _, err := networkauth.IssueClientRuntimeCredential(context.Background(), srv.Client(), srv.URL, memberToken, "project-alpha"); err != nil {
		t.Fatalf("IssueClientRuntimeCredential: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/network-auth/bundle?network_id=project-alpha", nil)
	if err != nil {
		t.Fatalf("NewRequest anonymous bundle: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("anonymous GET bundle: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("anonymous status = %d, want 401", resp.StatusCode)
	}

	req, err = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/network-auth/bundle?network_id=project-alpha", nil)
	if err != nil {
		t.Fatalf("NewRequest outsider bundle: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+outsiderToken)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("outsider GET bundle: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("outsider status = %d, want 403", resp.StatusCode)
	}

	req, err = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/network-auth/bundle?network_id=project-alpha", nil)
	if err != nil {
		t.Fatalf("NewRequest member bundle: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+memberToken)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("member GET bundle: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("member status = %d, want 200", resp.StatusCode)
	}
}

func TestVerifyRelayRuntimeCredentialRejectsReplay(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	state, err := loadOrCreateIssuerState(context.Background(), st, "project-alpha")
	if err != nil {
		t.Fatalf("loadOrCreateIssuerState: %v", err)
	}
	token, _, err := networkauth.SignRuntimeCredential(state, networkauth.SubjectKindClient, "github:1234", time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatalf("SignRuntimeCredential: %v", err)
	}

	replay := networkauth.NewReplayCache()
	if _, err := verifyRelayRuntimeCredential(context.Background(), st, token, replay); err != nil {
		t.Fatalf("verifyRelayRuntimeCredential first: %v", err)
	}
	if _, err := verifyRelayRuntimeCredential(context.Background(), st, token, replay); err == nil {
		t.Fatal("expected runtime credential replay to be rejected")
	}
}
