package relay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codewiresh/codewire/internal/networkauth"
	"github.com/codewiresh/codewire/internal/store"
)

func TestAccessGrantLifecycleRequiresOwnerAndPersists(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	ownerToken := createGitHubSession(t, st, 101, "owner")
	outsiderToken := createGitHubSession(t, st, 202, "outsider")
	createGitHubSession(t, st, 303, "alice")
	now := time.Now().UTC()
	if err := st.NetworkMemberUpsert(context.Background(), store.NetworkMember{
		NetworkID: "project-alpha",
		Subject:   "github:101",
		Role:      store.NetworkRoleOwner,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("NetworkMemberUpsert: %v", err)
	}

	srv := httptest.NewServer(buildMux(NewNodeHub(), NewPendingSessions(), st, RelayConfig{
		BaseURL:   "http://relay.test",
		AuthMode:  "token",
		AuthToken: "relay-admin",
	}, nil, networkauth.NewReplayCache(), nil))
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{
		"network_id":   "project-alpha",
		"target_node":  "dev-2",
		"session_name": "coder",
		"audience":     "alice",
		"verbs":        []string{"read", "listen"},
		"ttl":          "5m",
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/access-grants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+outsiderToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("outsider create access grant: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("outsider status = %d, want 403", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/v1/access-grants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("owner create access grant: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner status = %d", resp.StatusCode)
	}

	var issued networkauth.ObserverDelegationResponse
	if err := json.NewDecoder(resp.Body).Decode(&issued); err != nil {
		t.Fatalf("Decode create response: %v", err)
	}
	if issued.GrantID == "" {
		t.Fatal("expected grant id")
	}
	if issued.AudienceSubjectID != "github:303" {
		t.Fatalf("AudienceSubjectID = %q, want github:303", issued.AudienceSubjectID)
	}

	bundle, err := networkauth.FetchVerifierBundleWithToken(context.Background(), srv.Client(), srv.URL, "project-alpha", ownerToken)
	if err != nil {
		t.Fatalf("FetchVerifierBundleWithToken: %v", err)
	}
	claims, err := networkauth.VerifyObserverDelegation(issued.Delegation, bundle, time.Now().UTC())
	if err != nil {
		t.Fatalf("VerifyObserverDelegation: %v", err)
	}
	if claims.TargetNode != "dev-2" {
		t.Fatalf("TargetNode = %q", claims.TargetNode)
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/access-grants?network_id=project-alpha&active=true", nil)
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("list access grants: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	var grants []store.AccessGrant
	if err := json.NewDecoder(resp.Body).Decode(&grants); err != nil {
		t.Fatalf("Decode list response: %v", err)
	}
	if len(grants) != 1 || grants[0].ID != issued.GrantID {
		t.Fatalf("grants = %#v, want one active grant %q", grants, issued.GrantID)
	}

	req, _ = http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/access-grants/"+issued.GrantID+"?network_id=project-alpha", nil)
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("revoke access grant: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke status = %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/access-grants?network_id=project-alpha&active=true", nil)
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("list active access grants after revoke: %v", err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&grants); err != nil {
		t.Fatalf("Decode active list response: %v", err)
	}
	if len(grants) != 0 {
		t.Fatalf("active grants after revoke = %#v, want none", grants)
	}
}

func TestAccessGrantCreateRejectsAmbiguousAudience(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	ownerToken := createGitHubSession(t, st, 101, "owner")
	createGitHubSession(t, st, 303, "alice")
	now := time.Now().UTC()
	if err := st.NetworkMemberUpsert(context.Background(), store.NetworkMember{
		NetworkID: "project-alpha",
		Subject:   "github:101",
		Role:      store.NetworkRoleOwner,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("NetworkMemberUpsert: %v", err)
	}
	if err := st.OIDCUserUpsert(context.Background(), store.OIDCUser{
		Sub:         "oidc-alice",
		Username:    "alice",
		CreatedAt:   now,
		LastLoginAt: now,
	}); err != nil {
		t.Fatalf("OIDCUserUpsert: %v", err)
	}

	srv := httptest.NewServer(buildMux(NewNodeHub(), NewPendingSessions(), st, RelayConfig{
		BaseURL:   "http://relay.test",
		AuthMode:  "token",
		AuthToken: "relay-admin",
	}, nil, networkauth.NewReplayCache(), nil))
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{
		"network_id":   "project-alpha",
		"target_node":  "dev-2",
		"session_name": "coder",
		"audience":     "alice",
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/access-grants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("create ambiguous access grant: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestAccessGrantListMineReturnsAudienceScopedGrants(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	ownerToken := createGitHubSession(t, st, 101, "owner")
	aliceToken := createGitHubSession(t, st, 303, "alice")
	now := time.Now().UTC()
	if err := st.NetworkMemberUpsert(context.Background(), store.NetworkMember{
		NetworkID: "project-alpha",
		Subject:   "github:101",
		Role:      store.NetworkRoleOwner,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("NetworkMemberUpsert owner: %v", err)
	}
	if err := st.NetworkMemberUpsert(context.Background(), store.NetworkMember{
		NetworkID: "project-alpha",
		Subject:   "github:303",
		Role:      store.NetworkRoleMember,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("NetworkMemberUpsert alice: %v", err)
	}

	srv := httptest.NewServer(buildMux(NewNodeHub(), NewPendingSessions(), st, RelayConfig{
		BaseURL:   "http://relay.test",
		AuthMode:  "token",
		AuthToken: "relay-admin",
	}, nil, networkauth.NewReplayCache(), nil))
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{
		"network_id":   "project-alpha",
		"target_node":  "dev-2",
		"session_name": "coder",
		"audience":     "alice",
		"ttl":          "5m",
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/access-grants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("create access grant: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status = %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/access-grants?network_id=project-alpha&mine=true", nil)
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("list mine: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("mine status = %d", resp.StatusCode)
	}
	var grants []store.AccessGrant
	if err := json.NewDecoder(resp.Body).Decode(&grants); err != nil {
		t.Fatalf("Decode list mine: %v", err)
	}
	if len(grants) != 1 || grants[0].AudienceSubjectID != "github:303" {
		t.Fatalf("grants = %#v", grants)
	}
}

func TestAccessGrantEventStreamDeliversRevocations(t *testing.T) {
	st, err := store.NewSQLiteStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer st.Close()

	ownerToken := createGitHubSession(t, st, 101, "owner")
	aliceToken := createGitHubSession(t, st, 303, "alice")
	now := time.Now().UTC()
	if err := st.NetworkMemberUpsert(context.Background(), store.NetworkMember{
		NetworkID: "project-alpha",
		Subject:   "github:101",
		Role:      store.NetworkRoleOwner,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("NetworkMemberUpsert owner: %v", err)
	}
	if err := st.NetworkMemberUpsert(context.Background(), store.NetworkMember{
		NetworkID: "project-alpha",
		Subject:   "github:303",
		Role:      store.NetworkRoleMember,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("NetworkMemberUpsert alice: %v", err)
	}

	srv := httptest.NewServer(buildMux(NewNodeHub(), NewPendingSessions(), st, RelayConfig{
		BaseURL:   "http://relay.test",
		AuthMode:  "token",
		AuthToken: "relay-admin",
	}, nil, networkauth.NewReplayCache(), nil))
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{
		"network_id":   "project-alpha",
		"target_node":  "dev-2",
		"session_name": "coder",
		"audience":     "alice",
		"ttl":          "5m",
	})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/access-grants", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("create access grant: %v", err)
	}
	defer resp.Body.Close()
	var issued networkauth.ObserverDelegationResponse
	if err := json.NewDecoder(resp.Body).Decode(&issued); err != nil {
		t.Fatalf("Decode create response: %v", err)
	}

	streamReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/access/cache/events?network_id=project-alpha", nil)
	streamReq.Header.Set("Authorization", "Bearer "+aliceToken)
	streamReq.Header.Set("Accept", "text/event-stream")
	streamResp, err := srv.Client().Do(streamReq)
	if err != nil {
		t.Fatalf("watch access events: %v", err)
	}
	defer streamResp.Body.Close()
	if streamResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(streamResp.Body)
		t.Fatalf("stream status = %d body=%s", streamResp.StatusCode, strings.TrimSpace(string(body)))
	}

	eventCh := make(chan AccessCacheEvent, 1)
	errCh := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(streamResp.Body)
		var eventType string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				eventType = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
				continue
			}
			if strings.HasPrefix(line, "data: ") && eventType == "access.grant.revoked" {
				var ev AccessCacheEvent
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
					errCh <- err
					return
				}
				eventCh <- ev
				return
			}
		}
		if err := scanner.Err(); err != nil {
			errCh <- err
		}
	}()

	revokeReq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/access-grants/"+issued.GrantID+"?network_id=project-alpha", nil)
	revokeReq.Header.Set("Authorization", "Bearer "+ownerToken)
	revokeResp, err := srv.Client().Do(revokeReq)
	if err != nil {
		t.Fatalf("revoke access grant: %v", err)
	}
	revokeResp.Body.Close()
	if revokeResp.StatusCode != http.StatusOK {
		t.Fatalf("revoke status = %d", revokeResp.StatusCode)
	}

	select {
	case err := <-errCh:
		t.Fatalf("event stream error: %v", err)
	case ev := <-eventCh:
		if ev.Type != "access.grant.revoked" || ev.GrantID != issued.GrantID || ev.NetworkID != "project-alpha" {
			t.Fatalf("event = %#v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for access revoke event")
	}
}
