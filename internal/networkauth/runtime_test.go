package networkauth

import (
	"context"
	"testing"
	"time"
)

func TestSignAndVerifyRuntimeCredential(t *testing.T) {
	state, err := NewIssuerState("project-alpha")
	if err != nil {
		t.Fatalf("NewIssuerState: %v", err)
	}

	now := time.Now().UTC()
	token, claims, err := SignRuntimeCredential(state, SubjectKindClient, "github:1234", now, time.Minute)
	if err != nil {
		t.Fatalf("SignRuntimeCredential: %v", err)
	}

	bundle := state.Bundle(now, time.Hour)
	verified, err := VerifyRuntimeCredential(token, bundle, now)
	if err != nil {
		t.Fatalf("VerifyRuntimeCredential: %v", err)
	}
	if verified.SubjectKind != SubjectKindClient {
		t.Fatalf("SubjectKind = %q, want %q", verified.SubjectKind, SubjectKindClient)
	}
	if verified.SubjectID != "github:1234" {
		t.Fatalf("SubjectID = %q", verified.SubjectID)
	}
	if verified.JTI != claims.JTI {
		t.Fatalf("JTI = %q, want %q", verified.JTI, claims.JTI)
	}
}

func TestBundleCacheRefreshesOnExpiry(t *testing.T) {
	state, err := NewIssuerState("project-alpha")
	if err != nil {
		t.Fatalf("NewIssuerState: %v", err)
	}

	fetches := 0
	cache := NewBundleCache(func(context.Context) (*VerifierBundle, error) {
		fetches++
		validity := time.Hour
		if fetches == 1 {
			validity = 10 * time.Millisecond
		}
		return state.Bundle(time.Now().UTC(), validity), nil
	})

	if _, err := cache.Get(context.Background()); err != nil {
		t.Fatalf("Get first: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := cache.Get(context.Background()); err != nil {
		t.Fatalf("Get second: %v", err)
	}
	if fetches != 2 {
		t.Fatalf("fetches = %d, want 2", fetches)
	}
}

func TestSignAndVerifySenderDelegation(t *testing.T) {
	state, err := NewIssuerState("project-alpha")
	if err != nil {
		t.Fatalf("NewIssuerState: %v", err)
	}

	sessionID := uint32(17)
	now := time.Now().UTC()
	token, claims, err := SignSenderDelegation(state, "dev-1", &sessionID, "planner", []string{"msg", "request"}, "dev-2", now, time.Minute)
	if err != nil {
		t.Fatalf("SignSenderDelegation: %v", err)
	}

	bundle := state.Bundle(now, time.Hour)
	verified, err := VerifySenderDelegation(token, bundle, now)
	if err != nil {
		t.Fatalf("VerifySenderDelegation: %v", err)
	}
	if verified.SourceNode != "dev-1" {
		t.Fatalf("SourceNode = %q", verified.SourceNode)
	}
	if verified.FromSessionName != "planner" {
		t.Fatalf("FromSessionName = %q", verified.FromSessionName)
	}
	if verified.JTI != claims.JTI {
		t.Fatalf("JTI = %q, want %q", verified.JTI, claims.JTI)
	}
}
