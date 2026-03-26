package relay

import (
	"context"
	"encoding/json"
	"net/http/httptest"
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
	srv := httptest.NewServer(handler)
	defer srv.Close()

	bundle, err := networkauth.FetchVerifierBundle(context.Background(), srv.Client(), srv.URL, "project-alpha")
	if err != nil {
		t.Fatalf("FetchVerifierBundle: %v", err)
	}

	issued, err := networkauth.IssueClientRuntimeCredential(context.Background(), srv.Client(), srv.URL, "relay-admin", "project-alpha")
	if err != nil {
		t.Fatalf("IssueClientRuntimeCredential: %v", err)
	}
	if issued.SubjectKind != networkauth.SubjectKindClient {
		t.Fatalf("SubjectKind = %q", issued.SubjectKind)
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

	handler := buildMux(NewNodeHub(), NewPendingSessions(), st, RelayConfig{}, nil, networkauth.NewReplayCache(), nil)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	bundle, err := networkauth.FetchVerifierBundle(context.Background(), srv.Client(), srv.URL, "project-alpha")
	if err != nil {
		t.Fatalf("FetchVerifierBundle: %v", err)
	}

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

	handler := buildMux(NewNodeHub(), NewPendingSessions(), st, RelayConfig{}, nil, networkauth.NewReplayCache(), nil)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	bundle, err := networkauth.FetchVerifierBundle(context.Background(), srv.Client(), srv.URL, "project-alpha")
	if err != nil {
		t.Fatalf("FetchVerifierBundle: %v", err)
	}

	sessionID := uint32(42)
	issued, err := networkauth.IssueNodeSenderDelegation(context.Background(), srv.Client(), srv.URL, "node-token", "dev-1", &sessionID, "planner", []string{"msg"}, "dev-2")
	if err != nil {
		t.Fatalf("IssueNodeSenderDelegation: %v", err)
	}
	if issued.SourceNode != "dev-1" {
		t.Fatalf("SourceNode = %q", issued.SourceNode)
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
}

func TestVerifierBundleStableAcrossRequests(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	handler := buildMux(NewNodeHub(), NewPendingSessions(), st, RelayConfig{}, nil, networkauth.NewReplayCache(), nil)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	for i := 0; i < 2; i++ {
		resp, err := srv.Client().Get(srv.URL + "/api/v1/network-auth/bundle?network_id=project-alpha")
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
