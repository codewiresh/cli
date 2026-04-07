package client

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/codewiresh/codewire/internal/networkauth"
)

func TestAcceptAndResolveAccessGrant(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEWIRE_API_KEY", "")
	t.Setenv("CODEWIRE_RELAY_AUTH_TOKEN", "")
	t.Setenv("CODEWIRE_RELAY_URL", "")

	state, err := networkauth.NewIssuerState("project-alpha")
	if err != nil {
		t.Fatalf("NewIssuerState: %v", err)
	}

	token, claims, err := networkauth.SignObserverDelegation(state, "dev-2", nil, "coder", []string{"msg.read", "msg.listen"}, networkauth.SubjectKindClient, "github:303", time.Now().UTC(), 10*time.Minute)
	if err != nil {
		t.Fatalf("SignObserverDelegation: %v", err)
	}

	dir := t.TempDir()
	accepted, err := AcceptAccessGrant(dir, token)
	if err != nil {
		t.Fatalf("AcceptAccessGrant: %v", err)
	}
	if accepted.GrantID != claims.JTI {
		t.Fatalf("GrantID = %q, want %q", accepted.GrantID, claims.JTI)
	}

	resolved, err := ResolveAcceptedAccessGrant(dir, "project-alpha", "dev-2", nil, "coder", "msg.listen")
	if err != nil {
		t.Fatalf("ResolveAcceptedAccessGrant: %v", err)
	}
	if resolved != token {
		t.Fatalf("resolved token mismatch")
	}
}

func TestPruneAcceptedAccessGrantsRemovesRevokedAndMissing(t *testing.T) {
	t.Setenv("CODEWIRE_RELAY_URL", "http://relay.test")
	t.Setenv("CODEWIRE_API_KEY", "")
	t.Setenv("CODEWIRE_RELAY_AUTH_TOKEN", "dev-secret")

	state, err := networkauth.NewIssuerState("project-alpha")
	if err != nil {
		t.Fatalf("NewIssuerState: %v", err)
	}

	revokedToken, revokedClaims, err := networkauth.SignObserverDelegation(state, "dev-2", nil, "coder", []string{"msg.read"}, networkauth.SubjectKindClient, "github:303", time.Now().UTC(), 10*time.Minute)
	if err != nil {
		t.Fatalf("SignObserverDelegation revoked: %v", err)
	}
	missingToken, missingClaims, err := networkauth.SignObserverDelegation(state, "dev-2", nil, "coder", []string{"msg.listen"}, networkauth.SubjectKindClient, "github:303", time.Now().UTC(), 10*time.Minute)
	if err != nil {
		t.Fatalf("SignObserverDelegation missing: %v", err)
	}

	dir := t.TempDir()
	if _, err := AcceptAccessGrant(dir, revokedToken); err != nil {
		t.Fatalf("AcceptAccessGrant revoked: %v", err)
	}
	if _, err := AcceptAccessGrant(dir, missingToken); err != nil {
		t.Fatalf("AcceptAccessGrant missing: %v", err)
	}

	origClient := relayHTTPClient
	defer func() { relayHTTPClient = origClient }()
	srv := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer dev-secret" {
			t.Fatalf("Authorization = %q", got)
		}
		switch r.URL.Path {
		case "/api/v1/access-grants/" + revokedClaims.JTI:
			now := time.Now().UTC()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":                    revokedClaims.JTI,
				"network_id":            revokedClaims.NetworkID,
				"target_node":           revokedClaims.TargetNode,
				"session_name":          revokedClaims.SessionName,
				"verbs":                 revokedClaims.Verbs,
				"audience_subject_kind": revokedClaims.AudienceSubjectKind,
				"audience_subject_id":   revokedClaims.AudienceSubjectID,
				"created_at":            now.Add(-time.Minute),
				"expires_at":            revokedClaims.ExpiresAt,
				"revoked_at":            now,
			})
		case "/api/v1/access-grants/" + missingClaims.JTI:
			http.Error(w, "access grant not found", http.StatusNotFound)
		default:
			http.NotFound(w, r)
		}
	}))
	relayHTTPClient = srv.Client()
	t.Setenv("CODEWIRE_RELAY_URL", srv.URL)

	result, err := PruneAcceptedAccessGrants(dir, RelayAuthOptions{NetworkID: "project-alpha"})
	if err != nil {
		t.Fatalf("PruneAcceptedAccessGrants: %v", err)
	}
	if !result.RelayChecked || result.RemovedRevoked != 1 || result.RemovedMissing != 1 || result.Remaining != 0 {
		t.Fatalf("result = %#v", result)
	}

	grants, err := ListAcceptedAccessGrants(dir)
	if err != nil {
		t.Fatalf("ListAcceptedAccessGrants: %v", err)
	}
	if len(grants) != 0 {
		t.Fatalf("grants = %#v", grants)
	}
}

func TestResolveAcceptedAccessGrantRemovesMissingGrant(t *testing.T) {
	t.Setenv("CODEWIRE_API_KEY", "")
	t.Setenv("CODEWIRE_RELAY_AUTH_TOKEN", "dev-secret")

	state, err := networkauth.NewIssuerState("project-alpha")
	if err != nil {
		t.Fatalf("NewIssuerState: %v", err)
	}

	token, claims, err := networkauth.SignObserverDelegation(state, "dev-2", nil, "coder", []string{"msg.listen"}, networkauth.SubjectKindClient, "github:303", time.Now().UTC(), 10*time.Minute)
	if err != nil {
		t.Fatalf("SignObserverDelegation: %v", err)
	}

	dir := t.TempDir()
	if _, err := AcceptAccessGrant(dir, token); err != nil {
		t.Fatalf("AcceptAccessGrant: %v", err)
	}

	origClient := relayHTTPClient
	defer func() { relayHTTPClient = origClient }()
	srv := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "access grant not found", http.StatusNotFound)
	}))
	relayHTTPClient = srv.Client()
	t.Setenv("CODEWIRE_RELAY_URL", srv.URL)

	_, err = ResolveAcceptedAccessGrant(dir, "project-alpha", "dev-2", nil, "coder", "msg.listen")
	if err == nil {
		t.Fatal("expected missing accepted grant resolution to fail")
	}

	grants, err := ListAcceptedAccessGrants(dir)
	if err != nil {
		t.Fatalf("ListAcceptedAccessGrants: %v", err)
	}
	if len(grants) != 0 {
		t.Fatalf("grants after resolution = %#v (missing %s)", grants, claims.JTI)
	}
}

func TestListAcceptedAccessGrantsFiltered(t *testing.T) {
	state, err := networkauth.NewIssuerState("project-alpha")
	if err != nil {
		t.Fatalf("NewIssuerState: %v", err)
	}

	readToken, _, err := networkauth.SignObserverDelegation(state, "dev-2", nil, "coder", []string{"msg.read"}, networkauth.SubjectKindClient, "github:303", time.Now().UTC(), 10*time.Minute)
	if err != nil {
		t.Fatalf("SignObserverDelegation read: %v", err)
	}
	listenToken, _, err := networkauth.SignObserverDelegation(state, "dev-3", nil, "planner", []string{"msg.listen"}, networkauth.SubjectKindClient, "github:303", time.Now().UTC(), 10*time.Minute)
	if err != nil {
		t.Fatalf("SignObserverDelegation listen: %v", err)
	}

	dir := t.TempDir()
	if _, err := AcceptAccessGrant(dir, readToken); err != nil {
		t.Fatalf("AcceptAccessGrant read: %v", err)
	}
	if _, err := AcceptAccessGrant(dir, listenToken); err != nil {
		t.Fatalf("AcceptAccessGrant listen: %v", err)
	}

	grants, err := ListAcceptedAccessGrantsFiltered(dir, ListAcceptedAccessGrantOptions{
		TargetNode:  "dev-3",
		SessionName: "planner",
		Verb:        "msg.listen",
	})
	if err != nil {
		t.Fatalf("ListAcceptedAccessGrantsFiltered: %v", err)
	}
	if len(grants) != 1 || grants[0].TargetNode != "dev-3" || grants[0].SessionName != "planner" {
		t.Fatalf("grants = %#v", grants)
	}
}

func TestRemoveAcceptedAccessGrant(t *testing.T) {
	state, err := networkauth.NewIssuerState("project-alpha")
	if err != nil {
		t.Fatalf("NewIssuerState: %v", err)
	}

	token, claims, err := networkauth.SignObserverDelegation(state, "dev-2", nil, "coder", []string{"msg.read"}, networkauth.SubjectKindClient, "github:303", time.Now().UTC(), 10*time.Minute)
	if err != nil {
		t.Fatalf("SignObserverDelegation: %v", err)
	}

	dir := t.TempDir()
	if _, err := AcceptAccessGrant(dir, token); err != nil {
		t.Fatalf("AcceptAccessGrant: %v", err)
	}
	if err := RemoveAcceptedAccessGrant(dir, claims.JTI); err != nil {
		t.Fatalf("RemoveAcceptedAccessGrant: %v", err)
	}

	grants, err := ListAcceptedAccessGrants(dir)
	if err != nil {
		t.Fatalf("ListAcceptedAccessGrants: %v", err)
	}
	if len(grants) != 0 {
		t.Fatalf("grants = %#v", grants)
	}

	if err := RemoveAcceptedAccessGrant(dir, claims.JTI); err == nil {
		t.Fatal("expected removing missing accepted grant to fail")
	}
}

func TestGetAcceptedAccessGrant(t *testing.T) {
	state, err := networkauth.NewIssuerState("project-alpha")
	if err != nil {
		t.Fatalf("NewIssuerState: %v", err)
	}

	token, claims, err := networkauth.SignObserverDelegation(state, "dev-2", nil, "coder", []string{"msg.read"}, networkauth.SubjectKindClient, "github:303", time.Now().UTC(), 10*time.Minute)
	if err != nil {
		t.Fatalf("SignObserverDelegation: %v", err)
	}

	dir := t.TempDir()
	if _, err := AcceptAccessGrant(dir, token); err != nil {
		t.Fatalf("AcceptAccessGrant: %v", err)
	}

	grant, err := GetAcceptedAccessGrant(dir, claims.JTI)
	if err != nil {
		t.Fatalf("GetAcceptedAccessGrant: %v", err)
	}
	if grant.GrantID != claims.JTI || grant.TargetNode != "dev-2" {
		t.Fatalf("grant = %#v", grant)
	}

	if _, err := GetAcceptedAccessGrant(dir, "missing"); err == nil {
		t.Fatal("expected missing accepted grant lookup to fail")
	}
}

func TestWatchAcceptedAccessGrantsRemovesRevokedGrantAndPersistsCursor(t *testing.T) {
	t.Setenv("CODEWIRE_RELAY_URL", "http://relay.test")
	t.Setenv("CODEWIRE_API_KEY", "")
	t.Setenv("CODEWIRE_RELAY_AUTH_TOKEN", "dev-secret")
	t.Setenv("CODEWIRE_RELAY_NETWORK", "project-alpha")

	state, err := networkauth.NewIssuerState("project-alpha")
	if err != nil {
		t.Fatalf("NewIssuerState: %v", err)
	}

	token, claims, err := networkauth.SignObserverDelegation(state, "dev-2", nil, "coder", []string{"msg.listen"}, networkauth.SubjectKindClient, "github:303", time.Now().UTC(), 10*time.Minute)
	if err != nil {
		t.Fatalf("SignObserverDelegation: %v", err)
	}

	dir := t.TempDir()
	if _, err := AcceptAccessGrant(dir, token); err != nil {
		t.Fatalf("AcceptAccessGrant: %v", err)
	}

	streamStarted := make(chan struct{})
	streamClosed := make(chan struct{})
	origClient := relayHTTPClient
	defer func() { relayHTTPClient = origClient }()
	srv := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer dev-secret" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "text/event-stream" {
			t.Fatalf("Accept = %q", got)
		}
		if r.URL.Path != "/api/v1/access/cache/events" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("network_id"); got != "project-alpha" {
			t.Fatalf("network_id = %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("missing flusher")
		}
		close(streamStarted)
		_, _ = w.Write([]byte("id: 12\n"))
		_, _ = w.Write([]byte("event: access.grant.revoked\n"))
		_, _ = w.Write([]byte(`data: {"seq":12,"type":"access.grant.revoked","network_id":"project-alpha","grant_id":"` + claims.JTI + `"}` + "\n\n"))
		flusher.Flush()
		<-r.Context().Done()
		close(streamClosed)
	}))
	relayHTTPClient = srv.Client()
	t.Setenv("CODEWIRE_RELAY_URL", srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &strings.Builder{}
	done := make(chan error, 1)
	go func() {
		done <- WatchAcceptedAccessGrants(ctx, dir, RelayAuthOptions{NetworkID: "project-alpha"}, output)
	}()

	select {
	case <-streamStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for stream start")
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		grants, err := ListAcceptedAccessGrants(dir)
		if err != nil {
			t.Fatalf("ListAcceptedAccessGrants: %v", err)
		}
		if len(grants) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("grant was not removed by watcher: %#v", grants)
		}
		time.Sleep(25 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WatchAcceptedAccessGrants: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for watcher exit")
	}

	stateData, err := os.ReadFile(acceptedGrantStatePath(dir))
	if err != nil {
		t.Fatalf("ReadFile accepted grant state: %v", err)
	}
	if !strings.Contains(string(stateData), `"last_event_id": "12"`) {
		t.Fatalf("state file = %s", string(stateData))
	}
	if !strings.Contains(output.String(), claims.JTI) {
		t.Fatalf("watch output = %q", output.String())
	}

	select {
	case <-streamClosed:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for stream close")
	}
}
