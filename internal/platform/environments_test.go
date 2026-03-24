package platform

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestExecRequestHTTPTimeoutUsesMinimumWindow(t *testing.T) {
	got := execRequestHTTPTimeout(30)
	if got != 10*time.Minute {
		t.Fatalf("timeout = %s, want %s", got, 10*time.Minute)
	}
}

func TestExecRequestHTTPTimeoutFollowsLongerExecWindow(t *testing.T) {
	got := execRequestHTTPTimeout(900)
	want := 15*time.Minute + 30*time.Second
	if got != want {
		t.Fatalf("timeout = %s, want %s", got, want)
	}
}

func TestExecInEnvironmentDoesNotMutateBaseHTTPTimeout(t *testing.T) {
	client := &Client{
		ServerURL:    "https://example.invalid",
		SessionToken: "test-token",
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"exit_code":0}`))),
				}, nil
			}),
		},
	}

	if _, err := client.ExecInEnvironment("org_123", "env_123", &ExecRequest{
		Command: []string{"true"},
		Timeout: 180,
	}); err != nil {
		t.Fatalf("ExecInEnvironment: %v", err)
	}

	if client.HTTP.Timeout != 30*time.Second {
		t.Fatalf("client.HTTP.Timeout = %s, want %s", client.HTTP.Timeout, 30*time.Second)
	}
}
