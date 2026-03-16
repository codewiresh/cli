package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/codewiresh/codewire/internal/platform"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Acme Corp", "acme-corp"},
		{"My Cool Project", "my-cool-project"},
		{"  hello  world  ", "hello-world"},
		{"foo--bar", "foo-bar"},
		{"UPPER CASE", "upper-case"},
		{"special!@#chars", "special-chars"},
		{"-leading-trailing-", "leading-trailing"},
		{"a", "a"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := slugify(tt.input)
			if got != tt.want {
				t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestResolveOrgIDIgnoresDefaultOrgWhenAPIKeySet(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("CW_CONFIG_DIR", configDir)
	t.Setenv("CODEWIRE_API_KEY", "cw_api_key")

	if err := platform.SaveConfig(&platform.PlatformConfig{
		ServerURL:    "https://example.invalid",
		SessionToken: "session-token",
		DefaultOrg:   "org-default",
	}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	client := &platform.Client{
		ServerURL:    "https://example.invalid",
		SessionToken: "cw_api_key",
		HTTP: &http.Client{
			Timeout: 5 * time.Second,
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				body, _ := json.Marshal([]platform.OrgWithRole{
					{Organization: platform.Organization{ID: "org-api", Name: "API Org", Slug: "api-org"}},
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(bytes.NewReader(body)),
				}, nil
			}),
		},
	}

	orgID, err := resolveOrgID(client, "")
	if err != nil {
		t.Fatalf("resolveOrgID: %v", err)
	}
	if orgID != "org-api" {
		t.Fatalf("orgID = %q, want API-key org", orgID)
	}
}
